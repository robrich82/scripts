//go:build !windows

package main

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestAccessHint(t *testing.T) {
	denied := &os.PathError{Op: "open", Path: "cache.json", Err: fs.ErrPermission}
	if got := accessHint(denied, true); !strings.Contains(got, "file and its folder") {
		t.Errorf("save hint = %q; want it to name the file and its folder", got)
	}
	if got := accessHint(denied, false); !strings.Contains(got, "Permission denied") {
		t.Errorf("load hint = %q; want a permission hint", got)
	}
	if got := accessHint(&os.PathError{Op: "open", Path: "cache.json", Err: fs.ErrNotExist}, true); got != "" {
		t.Errorf("hint for a missing file = %q; want none", got)
	}
}
