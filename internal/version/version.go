// Package version holds the Thanos build version.
//
// The version is injected at build time via -ldflags:
//
//	go build -ldflags "-X thanos/internal/version.version=v1.0.0" ./cmd/thanos
//
// The build scripts (scripts/build.ps1, scripts/run.ps1) automatically
// derive the version from `git describe --tags`. To cut a new release:
//
//	git tag v1.2.0
//	.\scripts\build.ps1    # binary now reports v1.2.0
//
// If built without ldflags (e.g. `go run`), version falls back to "dev".
package version

// Version is the current build version. Overridden at build time via ldflags.
var Version = "dev"