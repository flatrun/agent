//go:build !linux

package securityreview

import (
	"fmt"
	"github.com/flatrun/agent/pkg/securityprofile"
)

type Snapshot struct {
	Path       string
	Omissions  []Omission
	Boundaries []Omission
	Files      int
	Bytes      int64
}

func SnapshotFiles(scratch, root string, paths []string, budget securityprofile.Budget) (Snapshot, error) {
	return Snapshot{}, fmt.Errorf("collector snapshots require Linux")
}
