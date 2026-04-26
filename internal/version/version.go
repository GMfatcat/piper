// Package version exposes the build-time version string injected via -ldflags.
package version

// Version is set by the build via -ldflags "-X github.com/GMfatcat/piper/internal/version.Version=..."
// Defaults to "dev" when built without ldflags.
var Version = "dev"
