package control

import (
	"strings"
	"testing"
)

func TestExtractManualCode(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCode  string
		wantState string
		wantErr   string
	}{
		{
			name:      "full URL with code and state",
			input:     "http://localhost:1461/callback?code=abc123&state=xyz789",
			wantCode:  "abc123",
			wantState: "xyz789",
		},
		{
			name:     "full URL with code only",
			input:    "http://localhost:1461/callback?code=abc123",
			wantCode: "abc123",
		},
		{
			name:    "URL with error param",
			input:   "http://localhost:1461/callback?error=access_denied",
			wantErr: "oauth error",
		},
		{
			name:    "URL with no query string",
			input:   "http://localhost:1461/callback",
			wantErr: "no query",
		},
		{
			name:     "raw code string",
			input:    "abc123defg456",
			wantCode: "abc123defg456",
		},
		{
			name:    "too short input",
			input:   "abc",
			wantErr: "too short",
		},
		{
			name:    "URL with no code param",
			input:   "http://localhost:1461/callback?foo=bar",
			wantErr: "no code",
		},
		{
			name:     "HTTPS URL",
			input:    "https://localhost:1461/callback?code=test123456",
			wantCode: "test123456",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, state, err := extractManualCode(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if code != tc.wantCode {
				t.Errorf("code: got %q, want %q", code, tc.wantCode)
			}
			if state != tc.wantState {
				t.Errorf("state: got %q, want %q", state, tc.wantState)
			}
		})
	}
}
