package main

import (
	"runtime"
	"slices"
	"testing"
)

func TestBuildUpdateInvocation(t *testing.T) {
	t.Run("plain command passes through", func(t *testing.T) {
		cmd, args := buildUpdateInvocation(&UpdateConfig{Command: "pip", Args: []string{"install", "--upgrade", "gamdl"}})
		if cmd != "pip" || !slices.Equal(args, []string{"install", "--upgrade", "gamdl"}) {
			t.Errorf("got (%q, %v)", cmd, args)
		}
	})

	t.Run("bat/cmd wrapping", func(t *testing.T) {
		cmd, args := buildUpdateInvocation(&UpdateConfig{Command: `C:\Program Files\mpv\updater.bat`})
		if runtime.GOOS == "windows" {
			want := []string{"/c", `C:\Program Files\mpv\updater.bat`}
			if cmd != "cmd" || !slices.Equal(args, want) {
				t.Errorf("got (%q, %v), want (cmd, %v)", cmd, args, want)
			}
		} else {
			if cmd != `C:\Program Files\mpv\updater.bat` || len(args) != 0 {
				t.Errorf("non-windows should pass through, got (%q, %v)", cmd, args)
			}
		}
	})
}

func TestJoinArgsForShell(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"-U"}, "-U"},
		{[]string{"/c", `C:\Program Files\mpv\updater.bat`}, `/c "C:\Program Files\mpv\updater.bat"`},
		{[]string{"install", "--upgrade", "gamdl"}, "install --upgrade gamdl"},
	}
	for _, tt := range tests {
		if got := joinArgsForShell(tt.args); got != tt.want {
			t.Errorf("joinArgsForShell(%v) = %q, want %q", tt.args, got, tt.want)
		}
	}
}
