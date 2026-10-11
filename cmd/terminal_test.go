package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowsTerminalColorHintRespectsOverrides(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "unknown terminal"},
		{name: "Windows Terminal without Unix hints", env: map[string]string{"WT_SESSION": "session"}, want: true},
		{name: "empty session", env: map[string]string{"WT_SESSION": ""}},
		{name: "blank session", env: map[string]string{"WT_SESSION": " "}},
		{name: "TERM hint", env: map[string]string{"WT_SESSION": "session", "TERM": "xterm-256color"}, want: true},
		{name: "COLORTERM hint", env: map[string]string{"WT_SESSION": "session", "COLORTERM": "truecolor"}, want: true},
		{name: "dumb terminal", env: map[string]string{"WT_SESSION": "session", "TERM": "dumb"}},
		{name: "NO_COLOR", env: map[string]string{"WT_SESSION": "session", "NO_COLOR": "1"}},
		{name: "empty NO_COLOR", env: map[string]string{"WT_SESSION": "session", "NO_COLOR": ""}},
		{name: "empty FORCE_COLOR", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": ""}},
		{name: "FORCE_COLOR false", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": " FALSE "}},
		{name: "FORCE_COLOR none", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": "0"}},
		{name: "FORCE_COLOR basic", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": "1"}},
		{name: "FORCE_COLOR 256", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": "2"}},
		{name: "FORCE_COLOR truecolor", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": "3"}},
		{name: "FORCE_COLOR clamped low", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": "-1"}},
		{name: "FORCE_COLOR clamped high", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": "99"}},
		{name: "padded FORCE_COLOR none", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": " 0 "}},
		{name: "padded FORCE_COLOR basic", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": " 1 "}},
		{name: "padded FORCE_COLOR 256", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": " 2 "}},
		{name: "padded FORCE_COLOR truecolor", env: map[string]string{"WT_SESSION": "session", "FORCE_COLOR": " 3 "}},
		{
			name: "padded FORCE_COLOR negative is unrecognized",
			env:  map[string]string{"WT_SESSION": "session", "FORCE_COLOR": " -1 "},
			want: true,
		},
		{
			name: "padded FORCE_COLOR high is unrecognized",
			env:  map[string]string{"WT_SESSION": "session", "FORCE_COLOR": " 99 "},
			want: true,
		},
		{
			name: "FORCE_COLOR true overrides NO_COLOR and dumb",
			env:  map[string]string{"WT_SESSION": "session", "FORCE_COLOR": " TRUE ", "NO_COLOR": "1", "TERM": "dumb"},
			want: true,
		},
		{
			name: "unrecognized FORCE_COLOR enables hints",
			env:  map[string]string{"WT_SESSION": "session", "FORCE_COLOR": "yes", "NO_COLOR": "1", "TERM": "dumb"},
			want: true,
		},
		{name: "FORCE_COLOR true without a known terminal", env: map[string]string{"FORCE_COLOR": "true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{"WT_SESSION", "TERM", "COLORTERM", "FORCE_COLOR", "NO_COLOR"} {
				// LookupEnv distinguishes an absent variable from an empty override.
				t.Setenv(key, "")
				require.NoError(t, os.Unsetenv(key))
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			assert.Equal(t, tt.want, windowsTerminalColorHintAllowed())
		})
	}
}
