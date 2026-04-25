// Package version exposes build-time version metadata for the SLTV binaries.
//
// Variables in this package are populated through linker flags
// (`-ldflags '-X github.com/sltv/sltv/internal/version.Version=...'`)
// when sltvd or sctl are built via the project Makefile.
package version

// Build metadata. Populated by -ldflags at link time. The defaults are
// useful when running `go run` during local development.
var (
	// Version is the semantic version or `git describe` output.
	Version = "dev"
	// Commit is the short Git commit hash.
	Commit = "none"
	// Date is the build timestamp in RFC3339 (UTC).
	Date = "unknown"
)

// String returns a human-readable summary of the build metadata.
func String() string {
	return Version + " (" + Commit + ", built " + Date + ")"
}
