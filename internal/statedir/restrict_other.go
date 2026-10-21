//go:build !windows

package statedir

import "os"

func restrict(dir string) error { return os.Chmod(dir, 0o700) }
