//go:build !darwin

package http

import (
	"context"
	"fmt"
	"os"

	"github.com/shirou/gopsutil/v4/process"
)

func listRuntimeOSProcesses(ctx context.Context) ([]runtimeOSProcess, error) {
	_ = ctx
	listed, err := process.Processes()
	if err != nil {
		return nil, err
	}
	result := make([]runtimeOSProcess, 0, len(listed))
	for _, current := range listed {
		command, commandErr := current.Cmdline()
		if commandErr != nil {
			continue
		}
		ppid, _ := current.Ppid()
		result = append(result, runtimeOSProcess{PID: int(current.Pid), PPID: int(ppid), Command: command})
	}
	return result, nil
}

func terminateRelatedRuntimeProcesses(ctx context.Context) (int, error) {
	_ = ctx
	return 0, nil
}

func restartWeChat(ctx context.Context) (int, error) {
	_ = ctx
	return 0, fmt.Errorf("WeChat restart is only configured for the macOS client")
}

func exitCurrentChatlogProcess() {
	os.Exit(0)
}
