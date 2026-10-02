package desktop

import "strings"

// updateAssetShape describes one release asset channel without naming a
// version, so a single shape matches both the version-stamped asset a release
// publishes now (Prism-0.9.1-Windows-x64.msi) and the unversioned name every
// release used before that (Prism-Windows-x64.msi).
//
// The unversioned name is still published, as a byte-identical alias, because
// an install shipped before version stamping matches release assets by exact
// name and would otherwise stop updating. See the "Release asset names" section
// of AGENTS.md.
type updateAssetShape struct {
	// Product is the leading name with its own casing: "Prism" for the
	// packages, "prism" for the portable Windows build.
	Product string
	// Tail is everything after the optional -<version>, including its leading
	// separator: ".exe", "-Windows-x64.msi", "-macOS.tar.gz".
	Tail string
}

// versionedName is the asset name a release tagged tag publishes.
func (s updateAssetShape) versionedName(tag string) string {
	return s.Product + "-" + assetVersion(tag) + s.Tail
}

// legacyName is the unversioned asset name releases used before version
// stamping, still published as an alias for older installs.
func (s updateAssetShape) legacyName() string {
	return s.Product + s.Tail
}

// matches reports whether name belongs to this channel, with or without a
// version stamp. Release tags are v<major>.<minor>.<build>; a leading "v" in
// the asset name is tolerated here so a hand-made tag still matches.
func (s updateAssetShape) matches(name string) bool {
	// A name shorter than both halves together can satisfy the prefix and
	// suffix checks at once; slicing the middle out of it would panic.
	if len(name) < len(s.Product)+len(s.Tail) {
		return false
	}
	if !strings.HasPrefix(name, s.Product) || !strings.HasSuffix(name, s.Tail) {
		return false
	}

	middle := name[len(s.Product) : len(name)-len(s.Tail)]
	if middle == "" {
		return true // the unversioned alias
	}
	if !strings.HasPrefix(middle, "-") {
		return false
	}
	return isNumericVersion(strings.TrimPrefix(middle[1:], "v"))
}

// isNumericVersion reports whether v is <major>.<minor>.<build>, all digits.
// A release tag that is anything else is rejected by the release workflow, so
// the same shape holds for every published asset name.
func isNumericVersion(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// assetVersion strips the leading "v" from a release tag, which is the form the
// tag takes in asset names: v0.9.1 -> 0.9.1.
func assetVersion(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "v")
}

// pickReleaseAsset returns the release asset this copy of Prism should
// download, or nil when the release carries none of the shapes.
//
// Shapes are tried in order of preference for how Prism was installed. Within a
// shape the version-stamped name for this release wins: that is the asset the
// release actually built, so a stale unversioned alias can never be chosen. The
// unversioned name is the fallback that keeps installs from before version
// stamping - and releases published before it - working.
func pickReleaseAsset(assets []GitHubReleaseAsset, shapes []updateAssetShape, tag string) *GitHubReleaseAsset {
	for _, shape := range shapes {
		want := shape.versionedName(tag)
		for i := range assets {
			if assets[i].Name == want {
				return &assets[i]
			}
		}
		for i := range assets {
			if shape.matches(assets[i].Name) {
				return &assets[i]
			}
		}
	}
	return nil
}

// expectedAssetNames lists the asset names a release is expected to carry for
// these shapes, for logs and error messages.
func expectedAssetNames(shapes []updateAssetShape, tag string) []string {
	names := make([]string, 0, len(shapes)*2)
	for _, shape := range shapes {
		names = append(names, shape.versionedName(tag), shape.legacyName())
	}
	return names
}
