package version

// Version is overridden at build time with -ldflags "-X procly/internal/version.Version=v0.1.0".
var Version = "0.1.0-dev"
