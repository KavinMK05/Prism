package admin

import "testing"

func TestAdminStaticCacheControl(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// Vite build output: content-hashed, so a year is safe.
		{"assets/index-B3iE4Sj9.js", "public, max-age=31536000, immutable"},
		{"assets/index-Cgn4Ut9d.css", "public, max-age=31536000, immutable"},
		{"assets/logo-Dx1y2z3a.svg", "public, max-age=31536000, immutable"},
		// Copied from web/public/: stable names, so they must revalidate.
		{"opencode-logo-light.svg", "no-cache"},
		{"hermes-icon.png", "no-cache"},
		{"prime-agent-logo.png", "no-cache"},
		{"favicon.ico", "no-cache"},
		// A nested directory that is not Vite's assets dir is not hashed either.
		{"img/index-B3iE4Sj9.js", "no-cache"},
		{"", "no-cache"},
	}

	for _, tt := range tests {
		if got := adminStaticCacheControl(tt.path); got != tt.want {
			t.Errorf("adminStaticCacheControl(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
