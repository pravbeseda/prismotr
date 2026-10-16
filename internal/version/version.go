// Package version carries the build version the binary reports.
package version

// Current is the version prismotr reports on --version. A variable rather than a constant
// because a release stamps it with -ldflags -X, which the linker ignores on a constant. The
// development default sorts below every release.
var Current = "0.0.0"
