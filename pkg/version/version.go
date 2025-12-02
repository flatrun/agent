package version

import (
	"os"
	"strings"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	BuildTime string `json:"build_time"`
	GitCommit string `json:"git_commit"`
}

func Get() Info {
	v := Version
	if v == "dev" {
		if data, err := os.ReadFile("VERSION"); err == nil {
			v = strings.TrimSpace(string(data)) + "-dev"
		}
	}
	return Info{
		Version:   v,
		BuildTime: BuildTime,
		GitCommit: GitCommit,
	}
}
