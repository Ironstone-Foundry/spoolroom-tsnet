//go:build windows

package statedir

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Run on Windows during P4: the state dir must have a protected DACL with
// exactly two entries (current user, SYSTEM).
func TestWindowsACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tsnet")
	if err := Prepare(dir); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if ctl&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("DACL inherits from parent")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl.AceCount != 2 {
		t.Fatalf("DACL has %d entries, want 2", dacl.AceCount)
	}
}
