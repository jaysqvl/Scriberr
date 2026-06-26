package version

// Build metadata is populated by GoReleaser and Docker builds via ldflags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
