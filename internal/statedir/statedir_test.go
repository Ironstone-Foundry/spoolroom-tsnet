package statedir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrepareCreatesPrivateDirWithMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tsnet")
	if err := Prepare(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, want 0700", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, Marker)); err != nil {
		t.Fatal("marker missing")
	}
	if err := Prepare(dir); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestPrepareTightensExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix modes")
	}
	dir := filepath.Join(t.TempDir(), "tsnet")
	os.Mkdir(dir, 0o755)
	if err := Prepare(dir); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestPrepareRefusesForeignDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "important.txt"), []byte("x"), 0o600)
	if err := Prepare(dir); err == nil {
		t.Fatal("adopted a non-empty foreign dir")
	}
	for _, d := range []string{"", "relative/dir", "/"} {
		if err := Prepare(d); err == nil {
			t.Errorf("accepted %q", d)
		}
	}
}

func TestWipeOnlyOurs(t *testing.T) {
	foreign := t.TempDir()
	os.WriteFile(filepath.Join(foreign, "keep"), []byte("x"), 0o600)
	if err := Wipe(foreign); err == nil {
		t.Fatal("wiped a dir without marker")
	}
	if _, err := os.Stat(filepath.Join(foreign, "keep")); err != nil {
		t.Fatal("foreign file removed")
	}
	ours := filepath.Join(t.TempDir(), "tsnet")
	Prepare(ours)
	os.WriteFile(filepath.Join(ours, "tailscaled.state"), []byte("{}"), 0o600)
	if err := Wipe(ours); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Fatal("state dir still present")
	}
}

func TestDefaultHostnamePersists(t *testing.T) {
	dir := t.TempDir()
	h1, err := DefaultHostname(dir, "spoolroom-watcher")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidHostname(h1) || len(h1) != len("spoolroom-watcher-")+4 {
		t.Fatalf("bad hostname %q", h1)
	}
	h2, _ := DefaultHostname(dir, "spoolroom-watcher")
	if h1 != h2 {
		t.Fatalf("hostname changed: %q -> %q", h1, h2)
	}
}

func TestValidHostname(t *testing.T) {
	for _, h := range []string{"spoolroom-watcher-ab12", "a", "x1"} {
		if !ValidHostname(h) {
			t.Errorf("rejected %q", h)
		}
	}
	for _, h := range []string{"", "-a", "a-", "Spoolroom", "a.b", "a_b", "mac mini"} {
		if ValidHostname(h) {
			t.Errorf("accepted %q", h)
		}
	}
}

func TestSymlinkRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	real := filepath.Join(t.TempDir(), "real")
	Prepare(real)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(link); err == nil {
		t.Fatal("Prepare accepted a symlinked dir")
	}
	if err := Wipe(link); err == nil {
		t.Fatal("Wipe followed a symlink")
	}
	if _, err := os.Stat(filepath.Join(real, Marker)); err != nil {
		t.Fatal("symlink target was touched")
	}
}

func TestRefusalLeavesModeAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix modes")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "important.txt"), []byte("x"), 0o644)
	if err := Prepare(dir); err == nil {
		t.Fatal("adopted a foreign dir")
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Fatalf("foreign dir mode changed to %v", fi.Mode().Perm())
	}
}
