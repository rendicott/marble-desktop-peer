package app

// Set at link time by release builds:
//
//	go build -ldflags "-X github.com/rendicott/marble-desktop-peer/internal/app.PeerVersion=v0.1.0 -X github.com/rendicott/marble-desktop-peer/internal/app.Commit=abc123 -X github.com/rendicott/marble-desktop-peer/internal/app.Date=2026-08-05T00:00:00Z"
var (
	// PeerVersion is the release tag or "dev" for local builds.
	PeerVersion = "dev"
	// Commit is the short git SHA when built via CI.
	Commit = "unknown"
	// Date is the UTC build timestamp when built via CI.
	Date = "unknown"
)

// VersionString is human-readable for CLI and logs.
func VersionString() string {
	return PeerVersion + " (commit " + Commit + ", built " + Date + ")"
}
