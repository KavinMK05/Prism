package config

import (
	"testing"
	"time"
)

func TestMCPOAuthTokenNeedsRefresh(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name  string
		token *MCPOAuthToken
		want  bool
	}{
		{
			name:  "nil token",
			token: nil,
			want:  false,
		},
		{
			name:  "no access token",
			token: &MCPOAuthToken{RefreshToken: "rt", ExpiresAt: now - 10},
			want:  false,
		},
		{
			name:  "no refresh token",
			token: &MCPOAuthToken{AccessToken: "at", ExpiresAt: now + 60},
			want:  false,
		},
		{
			name:  "no advertised expiry",
			token: &MCPOAuthToken{AccessToken: "at", RefreshToken: "rt"},
			want:  false,
		},
		{
			name:  "already expired",
			token: &MCPOAuthToken{AccessToken: "at", RefreshToken: "rt", ExpiresAt: now - 1},
			want:  true,
		},
		{
			name:  "ten minutes left on a one hour token",
			token: &MCPOAuthToken{AccessToken: "at", RefreshToken: "rt", IssuedAt: now - 3000, ExpiresAt: now + 600},
			want:  false,
		},
		{
			name:  "four minutes left on a one hour token",
			token: &MCPOAuthToken{AccessToken: "at", RefreshToken: "rt", IssuedAt: now - 3360, ExpiresAt: now + 240},
			want:  true,
		},
		{
			name:  "old token without issued_at uses the five minute window",
			token: &MCPOAuthToken{AccessToken: "at", RefreshToken: "rt", ExpiresAt: now + 240},
			want:  true,
		},
		{
			name:  "short token inside half its lifetime",
			token: &MCPOAuthToken{AccessToken: "at", RefreshToken: "rt", IssuedAt: now - 50, ExpiresAt: now + 10},
			want:  true,
		},
		{
			name:  "short token outside half its lifetime",
			token: &MCPOAuthToken{AccessToken: "at", RefreshToken: "rt", IssuedAt: now - 5, ExpiresAt: now + 55},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.NeedsRefresh(); got != tt.want {
				t.Errorf("NeedsRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}
