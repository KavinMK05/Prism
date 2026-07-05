package main

import "testing"

// realPythonStandaloneAssets mirrors the asset-name shape published by a real
// astral-sh/python-build-standalone release (here: tag 20260324), restricted to
// the macOS arm64 + Windows x86_64 install_only assets. It includes a 3.10 build
// (which must be skipped — SearXNG needs the stdlib `tomllib` from 3.11) plus
// 3.11/3.12/3.13/3.14 builds, so we can assert the selector picks the LOWEST
// version >=3.11.
func realPythonStandaloneAssets() []pythonStandaloneAsset {
	return []pythonStandaloneAsset{
		{Name: "cpython-3.10.20+20260324-aarch64-apple-darwin-install_only.tar.gz"},
		{Name: "cpython-3.11.15+20260324-aarch64-apple-darwin-install_only.tar.gz"},
		{Name: "cpython-3.12.13+20260324-aarch64-apple-darwin-install_only.tar.gz"},
		{Name: "cpython-3.13.7+20260324-aarch64-apple-darwin-install_only.tar.gz"},
		{Name: "cpython-3.14.1+20260324-aarch64-apple-darwin-install_only.tar.gz"},
		{Name: "cpython-3.10.20+20260324-x86_64-pc-windows-msvc-install_only.tar.gz"},
		{Name: "cpython-3.11.15+20260324-x86_64-pc-windows-msvc-install_only.tar.gz"},
		{Name: "cpython-3.12.13+20260324-x86_64-pc-windows-msvc-install_only.tar.gz"},
		{Name: "cpython-3.13.7+20260324-x86_64-pc-windows-msvc-install_only.tar.gz"},
		// PGO variant — must NOT match the install_only suffix.
		{Name: "cpython-3.13.7+20260324-aarch64-apple-darwin-pgo-install_only.tar.gz"},
		// Unrelated asset shape — must be skipped.
		{Name: "cpython-3.13.7+20260324-aarch64-apple-darwin-static.tar.gz"},
	}
}

// TestParseCpythonVersion covers the asset-name parser against the real format.
func TestParseCpythonVersion(t *testing.T) {
	cases := []struct {
		name       string
		maj, minor int
		ok         bool
	}{
		{"cpython-3.11.15+20260324-aarch64-apple-darwin-install_only.tar.gz", 3, 11, true},
		{"cpython-3.13.7+20260324-x86_64-pc-windows-msvc-install_only.tar.gz", 3, 13, true},
		{"cpython-3.10.20+20260324-aarch64-apple-darwin-install_only.tar.gz", 3, 10, true},
		{"cpython-3.14.1+20260324-aarch64-apple-darwin-pgo-install_only.tar.gz", 3, 14, true},
		{"cpythonfreethreaded-3.13.7+...", 0, 0, false},
		{"cpython-3.11+20260324-x86_64-pc-windows-msvc-install_only.tar.gz", 3, 11, true},
		{"not-a-python-asset.tar.gz", 0, 0, false},
	}
	for _, c := range cases {
		mj, mn, ok := parseCpythonVersion(c.name)
		if ok != c.ok || mj != c.maj || mn != c.minor {
			t.Fatalf("parseCpythonVersion(%q) = (%d,%d,%t), want (%d,%d,%t)",
				c.name, mj, mn, ok, c.maj, c.minor, c.ok)
		}
	}
}

// TestFindPythonAssetPicksLowestGe311 is the regression test for the SearXNG
// `tomllib` crash: the selector must skip the 3.10 build (which lacks the stdlib
// `tomllib` module) and pick the LOWEST Python >=3.11 available for the target,
// never an arbitrary first match.
func TestFindPythonAssetPicksLowestGe311(t *testing.T) {
	rel := &pythonStandaloneRelease{Assets: realPythonStandaloneAssets()}

	// Override the platform target to macOS arm64 for this test by setting the
	// helper via a small shim — searxngPythonTarget is platform-tagged, so we
	// build the expected suffix the same way the production code does.
	// We exercise the shared findPythonAsset directly with the real release
	// shape; the suffix is computed inside findPythonAsset, so we drive it by
	// running against the macOS-arm64 assets and asserting the chosen asset.
	// (findPythonAsset uses searxngPythonTarget(); on the test host that returns
	// the host platform, so we assert the chosen asset is the lowest >=3.11 for
	// whatever target the host is — verified by re-deriving the expected name.)
	got := findPythonAsset(rel)
	if got == nil {
		t.Fatal("findPythonAsset returned nil; expected a >=3.11 install_only asset")
	}
	mj, mn, ok := parseCpythonVersion(got.Name)
	if !ok {
		t.Fatalf("chosen asset %q failed version parse", got.Name)
	}
	if mj < 3 || (mj == 3 && mn < 11) {
		t.Fatalf("findPythonAsset picked %s (Python %d.%d); must be >=3.11 (tomllib requirement)",
			got.Name, mj, mn)
	}
	// Must match the host platform's install_only suffix.
	if !hasSuffix(got.Name, searxngPythonTarget()+"-install_only.tar.gz") {
		t.Fatalf("chosen asset %q does not match target suffix %s", got.Name, searxngPythonTarget())
	}
	// Must be the LOWEST >=3.11: find the minimum >=3.11 version present in the
	// fixture for this target and assert equality.
	wantMin := -1
	for _, a := range rel.Assets {
		if !hasSuffix(a.Name, searxngPythonTarget()+"-install_only.tar.gz") {
			continue
		}
		pmj, pmn, ok := parseCpythonVersion(a.Name)
		if !ok || pmj < 3 || (pmj == 3 && pmn < 11) {
			continue
		}
		score := pmj*1000 + pmn
		if wantMin == -1 || score < wantMin {
			wantMin = score
		}
	}
	if gotScore := mj*1000 + mn; gotScore != wantMin {
		t.Fatalf("findPythonAsset picked %s (score %d); want the lowest >=3.11 (score %d)",
			got.Name, gotScore, wantMin)
	}
}

// TestFindPythonAssetRejectsOnly310 asserts that when a release only has 3.10
// (no >=3.11), the selector returns nil rather than silently picking 3.10 — so
// the caller surfaces a clear "need Python >=3.11" error instead of crashing.
func TestFindPythonAssetRejectsOnly310(t *testing.T) {
	rel := &pythonStandaloneRelease{Assets: []pythonStandaloneAsset{
		{Name: "cpython-3.10.20+20260324-aarch64-apple-darwin-install_only.tar.gz"},
		{Name: "cpython-3.10.20+20260324-x86_64-pc-windows-msvc-install_only.tar.gz"},
	}}
	if a := findPythonAsset(rel); a != nil {
		t.Fatalf("findPythonAsset picked %s; must return nil when no >=3.11 asset exists", a.Name)
	}
}

// TestSystemPythonVersionOK asserts the floor is 3.11 (tomllib), not 3.10.
func TestSystemPythonVersionOK(t *testing.T) {
	good := []string{"3.11", "3.11.5", "3.12", "3.13.7", "4.0.0"}
	for _, v := range good {
		if !systemPythonVersionOK(v) {
			t.Fatalf("systemPythonVersionOK(%q) = false, want true (>=3.11)", v)
		}
	}
	bad := []string{"3.10", "3.10.18", "3.9.2", "2.7", "3"}
	for _, v := range bad {
		if systemPythonVersionOK(v) {
			t.Fatalf("systemPythonVersionOK(%q) = true, want false (<3.11; SearXNG needs tomllib)", v)
		}
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}