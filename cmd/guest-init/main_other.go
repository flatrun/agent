//go:build !linux

package main

import "log"

func main() { log.Fatal("guest init requires Linux") }
