package desktop

import (
	"reflect"
	"testing"
)

// The channel shapes, spelled out here exactly as update_windows.go,
// update_darwin.go and update_linux.go declare them, so a filename change is a
// test failure rather than a release that quietly stops updating.
var (
	testPortableShape = updateAssetShape{Product: "prism", Tail: ".exe"}
	testMSIShape      = updateAssetShape{Product: "Prism", Tail: "-Windows-x64.msi"}
	testDarwinShape   = updateAssetShape{Product: "Prism", Tail: "-macOS.tar.gz"}
	testLinuxTarShape = updateAssetShape{Product: "Prism", Tail: "-Linux.tar.gz"}
	testAppImageShape = updateAssetShape{Product: "Prism", Tail: "-Linux-x86_64.AppImage"}
	testARMAppImage   = updateAssetShape{Product: "Prism", Tail: "-Linux-aarch64.AppImage"}
)

func TestUpdateAssetShapeVersionedName(t *testing.T) {
	tests := []struct {
		shape updateAssetShape
		tag   string
		want  string
	}{
		{testPortableShape, "v0.9.1", "prism-0.9.1.exe"},
		{testPortableShape, "0.9.1", "prism-0.9.1.exe"},
		{testPortableShape, " v0.9.1 ", "prism-0.9.1.exe"},
		{testMSIShape, "v0.9.1", "Prism-0.9.1-Windows-x64.msi"},
		{testDarwinShape, "v0.9.1", "Prism-0.9.1-macOS.tar.gz"},
		{testLinuxTarShape, "v0.9.1", "Prism-0.9.1-Linux.tar.gz"},
		{testAppImageShape, "v0.9.1", "Prism-0.9.1-Linux-x86_64.AppImage"},
		{testARMAppImage, "v1.2.3", "Prism-1.2.3-Linux-aarch64.AppImage"},
		// The pull-request version token the workflow uses on non-tag runs.
		{testMSIShape, "0.0.1234", "Prism-0.0.1234-Windows-x64.msi"},
	}

	for _, tt := range tests {
		if got := tt.shape.versionedName(tt.tag); got != tt.want {
			t.Errorf("%s.versionedName(%q) = %q, want %q", tt.shape.Product+tt.shape.Tail, tt.tag, got, tt.want)
		}
	}
}

func TestUpdateAssetShapeLegacyName(t *testing.T) {
	tests := []struct {
		shape updateAssetShape
		want  string
	}{
		{testPortableShape, "prism.exe"},
		{testMSIShape, "Prism-Windows-x64.msi"},
		{testDarwinShape, "Prism-macOS.tar.gz"},
		{testLinuxTarShape, "Prism-Linux.tar.gz"},
		{testAppImageShape, "Prism-Linux-x86_64.AppImage"},
		{testARMAppImage, "Prism-Linux-aarch64.AppImage"},
	}

	for _, tt := range tests {
		if got := tt.shape.legacyName(); got != tt.want {
			t.Errorf("legacyName() = %q, want %q", got, tt.want)
		}
	}
}

func TestUpdateAssetShapeMatches(t *testing.T) {
	tests := []struct {
		shape updateAssetShape
		name  string
		want  bool
	}{
		// The portable Windows build: the unversioned alias and the versioned
		// name both have to match.
		{testPortableShape, "prism.exe", true},
		{testPortableShape, "prism-0.9.1.exe", true},
		{testPortableShape, "prism-v0.9.1.exe", true},
		{testPortableShape, "prism-0.0.1234.exe", true},
		{testPortableShape, "Prism.exe", false},       // casing is part of the contract
		{testPortableShape, "prism-dev.exe", false},   // never a release asset
		{testPortableShape, "prism-0.9.1.zip", false}, //
		{testPortableShape, "prism-0.9.exe", false},   // two-field version
		{testPortableShape, "prism-0.9.1.2.exe", false},
		{testPortableShape, "prism-0.9.1-beta.exe", false},
		{testPortableShape, "myprism.exe", false},
		{testPortableShape, "prism", false}, // shorter than Product+Tail

		{testMSIShape, "Prism-Windows-x64.msi", true},
		{testMSIShape, "Prism-0.9.1-Windows-x64.msi", true},
		{testMSIShape, "Prism-v0.9.1-Windows-x64.msi", true},
		{testMSIShape, "Prism-Windows-x64.dmg", false},
		{testMSIShape, "Prism-1.0.0-Windows-x64.msi.bak", false},
		{testMSIShape, "Prism-Windows-x64.msi.sig", false},
		{testMSIShape, "Prism-Windows-arm64.msi", false},

		{testDarwinShape, "Prism-macOS.tar.gz", true},
		{testDarwinShape, "Prism-0.9.1-macOS.tar.gz", true},
		{testDarwinShape, "Prism-0.9.1-macOS.dmg", false},
		{testDarwinShape, "Prism-macOS.zip", false},

		{testLinuxTarShape, "Prism-Linux.tar.gz", true},
		{testLinuxTarShape, "Prism-0.9.1-Linux.tar.gz", true},
		{testLinuxTarShape, "Prism-Linux.tar.gz.asc", false},

		{testAppImageShape, "Prism-Linux-x86_64.AppImage", true},
		{testAppImageShape, "Prism-0.9.1-Linux-x86_64.AppImage", true},
		{testAppImageShape, "Prism-Linux-aarch64.AppImage", false},
		{testAppImageShape, "Prism-0.9.1-Linux.tar.gz", false},
	}

	for _, tt := range tests {
		if got := tt.shape.matches(tt.name); got != tt.want {
			t.Errorf("matches(%q) for %q = %v, want %v", tt.name, tt.shape.Product+tt.shape.Tail, got, tt.want)
		}
	}
}

func TestIsNumericVersion(t *testing.T) {
	for _, want := range []string{"0.9.1", "10.20.30000", "0.0.1234"} {
		if !isNumericVersion(want) {
			t.Errorf("isNumericVersion(%q) = false, want true", want)
		}
	}
	for _, want := range []string{"", "0.9", "0.9.1.2", "0.9.1-beta", "v0.9.1", "a.b.c", "0..1"} {
		if isNumericVersion(want) {
			t.Errorf("isNumericVersion(%q) = true, want false", want)
		}
	}
}

func TestPickReleaseAsset(t *testing.T) {
	asset := func(name string) GitHubReleaseAsset {
		return GitHubReleaseAsset{Name: name, BrowserDownloadURL: "https://example.invalid/" + name, Size: 42}
	}

	t.Run("prefers the versioned asset", func(t *testing.T) {
		// A stale alias in a release must never win over the asset the release
		// actually built for this tag.
		assets := []GitHubReleaseAsset{asset("Prism-Windows-x64.msi"), asset("Prism-0.9.1-Windows-x64.msi")}
		got := pickReleaseAsset(assets, []updateAssetShape{testMSIShape}, "v0.9.1")
		if got == nil || got.Name != "Prism-0.9.1-Windows-x64.msi" {
			t.Fatalf("pickReleaseAsset() = %v, want the versioned MSI", got)
		}
	})

	t.Run("falls back to the unversioned alias", func(t *testing.T) {
		// The compatibility path: an old release, or a new one whose alias is
		// all that survived, still updates.
		assets := []GitHubReleaseAsset{asset("Prism-Windows-x64.msi")}
		got := pickReleaseAsset(assets, []updateAssetShape{testMSIShape}, "v0.9.1")
		if got == nil || got.Name != "Prism-Windows-x64.msi" {
			t.Fatalf("pickReleaseAsset() = %v, want the legacy MSI", got)
		}
	})

	t.Run("matches the portable exe", func(t *testing.T) {
		assets := []GitHubReleaseAsset{asset("prism.exe")}
		got := pickReleaseAsset(assets, []updateAssetShape{testPortableShape}, "v0.9.1")
		if got == nil || got.Name != "prism.exe" {
			t.Fatalf("pickReleaseAsset() = %v, want prism.exe", got)
		}
	})

	t.Run("an MSI install never falls back to the portable exe", func(t *testing.T) {
		assets := []GitHubReleaseAsset{asset("prism-0.9.1.exe")}
		if got := pickReleaseAsset(assets, []updateAssetShape{testMSIShape}, "v0.9.1"); got != nil {
			t.Fatalf("pickReleaseAsset() = %v, want nil: an MSI install must not replace an installer-owned file", got)
		}
	})

	t.Run("honours shape order", func(t *testing.T) {
		assets := []GitHubReleaseAsset{asset("prism-0.9.1.exe"), asset("Prism-0.9.1-Windows-x64.msi")}
		shapes := []updateAssetShape{testMSIShape, testPortableShape}
		got := pickReleaseAsset(assets, shapes, "v0.9.1")
		if got == nil || got.Name != "Prism-0.9.1-Windows-x64.msi" {
			t.Fatalf("pickReleaseAsset() = %v, want the first shape's asset", got)
		}
	})

	t.Run("no match returns nil", func(t *testing.T) {
		assets := []GitHubReleaseAsset{asset("Prism-0.9.1-macOS.dmg"), asset("notes.txt")}
		if got := pickReleaseAsset(assets, []updateAssetShape{testMSIShape}, "v0.9.1"); got != nil {
			t.Fatalf("pickReleaseAsset() = %v, want nil", got)
		}
	})

	t.Run("empty asset list returns nil", func(t *testing.T) {
		if got := pickReleaseAsset(nil, []updateAssetShape{testMSIShape}, "v0.9.1"); got != nil {
			t.Fatalf("pickReleaseAsset() = %v, want nil", got)
		}
	})

	t.Run("accepts a leading v in the asset name", func(t *testing.T) {
		assets := []GitHubReleaseAsset{asset("Prism-v0.9.1-macOS.tar.gz")}
		got := pickReleaseAsset(assets, []updateAssetShape{testDarwinShape}, "v0.9.1")
		if got == nil || got.Name != "Prism-v0.9.1-macOS.tar.gz" {
			t.Fatalf("pickReleaseAsset() = %v, want the v-prefixed asset", got)
		}
	})
}

func TestExpectedAssetNames(t *testing.T) {
	got := expectedAssetNames([]updateAssetShape{testMSIShape, testPortableShape}, "v0.9.1")
	want := []string{
		"Prism-0.9.1-Windows-x64.msi", "Prism-Windows-x64.msi",
		"prism-0.9.1.exe", "prism.exe",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expectedAssetNames() = %v, want %v", got, want)
	}

	if got := expectedAssetNames(nil, "v0.9.1"); len(got) != 0 {
		t.Fatalf("expectedAssetNames(nil) = %v, want empty", got)
	}
}
