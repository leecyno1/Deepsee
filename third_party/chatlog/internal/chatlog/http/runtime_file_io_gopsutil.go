//go:build !darwin

package http

import (
	"fmt"

	"github.com/shirou/gopsutil/v4/process"
)

func readRuntimeProcessFileIO(pid int) (uint64, uint64, error) {
	current, err := process.NewProcess(int32(pid))
	if err != nil {
		return 0, 0, err
	}
	counters, err := current.IOCounters()
	if err != nil || counters == nil {
		if err == nil {
			err = fmt.Errorf("process I/O counters are empty")
		}
		return 0, 0, err
	}
	return counters.ReadBytes, counters.WriteBytes, nil
}
