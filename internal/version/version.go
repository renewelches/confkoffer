package version

// Build metadata, injected at link time by goreleaser (see
// .goreleaser.yaml) or by `make build`.
//
// Date must not default to time.Now(): that reports the moment the
// binary *ran*, which reads as a build date and is wrong for every
// build that did not set it. "unknown" is the honest answer, and it is
// visibly not a date, so nobody quotes it in a bug report.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)
