//go:build !darwin

package runtimes

import "os"

func offlineTartNetwork() (*os.File, *os.File, error) { return nil, nil, ErrUnavailable }
