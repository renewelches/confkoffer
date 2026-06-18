package version

import "time"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = time.Now().Local().Format("2006-01-02T15:04:05 -07:00:00")
)
