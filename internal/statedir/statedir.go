// Package statedir owns the helper's state directory: mode 0700, a marker
// file so a wipe can never remove a directory that is not ours, and the
// persisted short hostname suffix.
package statedir

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Marker is created in every directory the helper owns.
const Marker = ".spoolroom-tsnet"

const shortFile = "hostname-suffix"

// Prepare creates dir and restricts it to the current user (mode 0700, or
// an owner+SYSTEM ACL on Windows), then claims it with the marker. A
// symlink, or a non-empty directory without the marker, is refused before
// anything about it is changed, so a wrong --dir is never adopted, tightened
// or later wiped.
func Prepare(dir string) error {
	if err := checkPath(dir); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("state dir %s is a symlink; pass the real path", dir)
	case !fi.IsDir():
		return fmt.Errorf("state dir %s is not a directory", dir)
	}
	_, markErr := os.Stat(filepath.Join(dir, Marker))
	if markErr != nil {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(ents) > 0 {
			return fmt.Errorf("state dir %s is not empty and was not created by spoolroom-tsnet", dir)
		}
	}
	if err := restrict(dir); err != nil {
		return fmt.Errorf("restrict state dir: %w", err)
	}
	if markErr == nil {
		return nil
	}
	return os.WriteFile(filepath.Join(dir, Marker), []byte("spoolroom-tsnet state; safe to delete to sign out\n"), 0o600)
}

// Wipe removes dir, but only if it is a real directory carrying the marker.
func Wipe(dir string) error {
	if err := checkPath(dir); err != nil {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil {
		return err
	} else if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("refusing to wipe %s: not a plain directory", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, Marker)); err != nil {
		return fmt.Errorf("refusing to wipe %s: no %s marker", dir, Marker)
	}
	return os.RemoveAll(dir)
}

func checkPath(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("state dir must be an absolute path")
	}
	clean := filepath.Clean(dir)
	if clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return errors.New("state dir must not be a filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == clean {
		return errors.New("state dir must not be the home directory")
	}
	return nil
}

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHostname reports whether h is a usable single DNS label.
func ValidHostname(h string) bool { return hostRe.MatchString(h) }

// DefaultHostname returns prefix-<4 base32 chars>, persisting the suffix in
// dir so the node keeps its name across restarts. The suffix is random, not
// derived from anything about the member, because certificate names are
// published in Certificate Transparency logs.
func DefaultHostname(dir, prefix string) (string, error) {
	p := filepath.Join(dir, shortFile)
	if b, err := os.ReadFile(p); err == nil {
		s := strings.TrimSpace(string(b))
		if h := prefix + "-" + s; len(s) == 4 && ValidHostname(h) {
			return h, nil
		}
	}
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	s := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))[:4]
	if err := os.WriteFile(p, []byte(s+"\n"), 0o600); err != nil {
		return "", err
	}
	return prefix + "-" + s, nil
}
