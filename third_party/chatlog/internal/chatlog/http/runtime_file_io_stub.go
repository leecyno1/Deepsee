//go:build darwin && !cgo

package http

import "fmt"

func readRuntimeProcessFileIO(pid int) (uint64, uint64, error) {
	return 0, 0, fmt.Errorf("process file I/O metrics require a CGO-enabled macOS build (pid %d)", pid)
}
