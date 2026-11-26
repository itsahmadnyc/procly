// Package version reports which procly this is.
package version

import (
	"runtime/debug"
	"strings"
)

// Version is set by release builds:
//
//	go build -ldflags "-X github.com/itsahmadnyc/procly/internal/version.Version=0.1.0"
//
// Otherwise it comes from the Go module version (`go install ...@v0.1.0`),
// or stays "dev" for a plain local build.
var Version = "dev"

func init() {
	if Version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		Version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}
