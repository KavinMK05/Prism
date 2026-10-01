package platform

import (
	"runtime"
	"testing"
)

func TestSameExecutablePath(t *testing.T) {
	const exe = "/opt/prism/prism"
	quote := string('"')

	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", exe, exe, true},
		{"quoted entry", quote + exe + quote, exe, true},
		{"surrounding whitespace", "  " + exe + "\n", exe, true},
		{"redundant separators", "/opt/prism/./prism", exe, true},
		{"different binary", "/opt/prism/other", exe, false},
		{"empty entry", "", exe, false},
		{"parent directory only", "/opt/prism", exe, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameExecutablePath(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameExecutablePath(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}

	// Case is ignored only where the filesystem ignores it.
	caseFold := sameExecutablePath("/opt/Prism/PRISM", "/opt/prism/prism")
	if runtime.GOOS == "windows" && !caseFold {
		t.Fatal("windows paths must compare case-insensitively")
	}
	if runtime.GOOS != "windows" && caseFold {
		t.Fatal("paths differing in case must not compare equal off windows")
	}
}

func TestPlistProgramPath(t *testing.T) {
	// The Label value is also a <string> element and comes first, so a naive
	// scan would return "com.prism" and repoint the LaunchAgent on every launch.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.prism</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/kavin/Applications/Prism.app/Contents/MacOS/prism</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>`

	want := "/Users/kavin/Applications/Prism.app/Contents/MacOS/prism"
	if got := plistProgramPath(plist); got != want {
		t.Fatalf("plistProgramPath = %q, want %q", got, want)
	}

	if got := plistProgramPath("<plist><key>Label</key><string>com.prism</string></plist>"); got != "" {
		t.Fatalf("plistProgramPath without ProgramArguments = %q, want empty", got)
	}
}

func TestDesktopExecPath(t *testing.T) {
	entry := `[Desktop Entry]
Type=Application
Name=Prism
Comment=Prism local AI proxy
Exec=/home/kavin/My Apps/prism
Terminal=false
X-GNOME-Autostart-enabled=true
`

	// The whole remainder of the line is kept, so a path containing spaces still
	// matches what SetAutoStart wrote.
	if got, want := desktopExecPath(entry), "/home/kavin/My Apps/prism"; got != want {
		t.Fatalf("desktopExecPath = %q, want %q", got, want)
	}

	if got := desktopExecPath("[Desktop Entry]\nName=Prism\n"); got != "" {
		t.Fatalf("desktopExecPath without Exec = %q, want empty", got)
	}
}
