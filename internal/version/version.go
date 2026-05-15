// Package version holds the build-time version string for the agora
// binary. Default is "dev"; release builds override via -ldflags:
//
//	go build -ldflags "-X github.com/CarriedWorldUniverse/agora/internal/version.Version=v0.1.0" ./cmd/agora
//
// CI invokes `git describe --tags --always --dirty` for the value so
// dev builds report e.g. v0.1.0-3-gabc1234, release builds report the
// clean tag (v0.1.0).
package version

// Version is the build-time version string. Overridden via -ldflags at
// build time; "dev" when unset.
var Version = "dev"
