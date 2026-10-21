package main

import (
	_ "embed"
	"runtime/debug"
)

// version is set at release build time with -ldflags "-X main.version=…".
var version = "dev"

//go:embed LICENSE
var licenseText string

//go:embed THIRD_PARTY_NOTICES.md
var noticesText string

func licenses() string { return licenseText + "\n\n" + noticesText }

// tailscaleVersion reports the linked tailscale.com module version.
func tailscaleVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, m := range bi.Deps {
			if m.Path == "tailscale.com" {
				return m.Version
			}
		}
	}
	return "unknown"
}
