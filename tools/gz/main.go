// Command gz writes a byte-for-byte reproducible gzip of each file (no
// name, no timestamp), so release hashes match on any build machine with
// the same Go toolchain. System gzip implementations differ in output.
package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
)

func main() {
	for _, p := range os.Args[1:] {
		if err := gz(p); err != nil {
			fmt.Fprintln(os.Stderr, "gz:", err)
			os.Exit(1)
		}
	}
}

func gz(p string) error {
	in, err := os.Open(p)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(p + ".gz")
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := io.Copy(zw, in); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return out.Close()
}
