package runtimes

import (
	"os"

	"golang.org/x/sys/unix"
)

// Frames from an offline VM terminate at this socket pair. Neither end connects to a host network.
func offlineTartNetwork() (*os.File, *os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, err
	}
	if err = unix.SetNonblock(pair[1], true); err != nil {
		unix.Close(pair[0])
		unix.Close(pair[1])
		return nil, nil, err
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	return os.NewFile(uintptr(pair[0]), "tart-network"), os.NewFile(uintptr(pair[1]), "tart-offline"), nil
}
