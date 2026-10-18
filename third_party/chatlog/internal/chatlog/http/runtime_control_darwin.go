//go:build darwin

package http

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func listRuntimeOSProcesses(ctx context.Context) ([]runtimeOSProcess, error) {
	output, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	processes := make([]runtimeOSProcess, 0, 128)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil || pid <= 0 {
			continue
		}
		commandIndex := strings.Index(line, fields[2])
		if commandIndex < 0 {
			continue
		}
		processes = append(processes, runtimeOSProcess{
			PID:     pid,
			PPID:    ppid,
			Command: strings.TrimSpace(line[commandIndex:]),
		})
	}
	return processes, nil
}

func terminateRelatedRuntimeProcesses(ctx context.Context) (int, error) {
	processes, err := listRuntimeOSProcesses(ctx)
	if err != nil {
		return 0, err
	}
	byPID := make(map[int]runtimeOSProcess, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
	}

	// Keep the current process and its launcher chain alive until cleanup and the
	// WeChat restart have completed. The current process exits last.
	protected := map[int]struct{}{os.Getpid(): {}}
	for pid := os.Getppid(); pid > 1; {
		if _, seen := protected[pid]; seen {
			break
		}
		protected[pid] = struct{}{}
		process, ok := byPID[pid]
		if !ok {
			break
		}
		pid = process.PPID
	}

	targets := make(map[int]struct{})
	for _, process := range processes {
		if _, keep := protected[process.PID]; keep {
			continue
		}
		if relatedRuntimeProcess(process.Command) {
			targets[process.PID] = struct{}{}
		}
	}
	// Include children such as Python resource trackers and Frida helpers even
	// when their command line does not repeat the parent marker.
	for changed := true; changed; {
		changed = false
		for _, process := range processes {
			if _, keep := protected[process.PID]; keep {
				continue
			}
			if _, exists := targets[process.PID]; exists {
				continue
			}
			if _, parentTarget := targets[process.PPID]; parentTarget {
				targets[process.PID] = struct{}{}
				changed = true
			}
		}
	}
	targetCount := len(targets)

	var terminateErrors []error
	for pid := range targets {
		if signalErr := syscall.Kill(pid, syscall.SIGTERM); signalErr != nil && !errors.Is(signalErr, syscall.ESRCH) {
			terminateErrors = append(terminateErrors, fmt.Errorf("terminate pid %d: %w", pid, signalErr))
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(targets) > 0 && time.Now().Before(deadline) && ctx.Err() == nil {
		for pid := range targets {
			if processSignalMissing(pid) {
				delete(targets, pid)
			}
		}
		if len(targets) > 0 {
			time.Sleep(80 * time.Millisecond)
		}
	}
	for pid := range targets {
		if signalErr := syscall.Kill(pid, syscall.SIGKILL); signalErr != nil && !errors.Is(signalErr, syscall.ESRCH) {
			terminateErrors = append(terminateErrors, fmt.Errorf("kill pid %d: %w", pid, signalErr))
		}
	}
	return targetCount, errors.Join(terminateErrors...)
}

func processSignalMissing(pid int) bool {
	err := syscall.Kill(pid, syscall.Signal(0))
	return errors.Is(err, syscall.ESRCH)
}

func restartWeChat(ctx context.Context) (int, error) {
	processes, err := listRuntimeOSProcesses(ctx)
	if err != nil {
		return 0, err
	}
	oldPIDs := make(map[int]struct{})
	for _, process := range processes {
		if isWeChatMainProcess(process.Command) {
			oldPIDs[process.PID] = struct{}{}
			_ = syscall.Kill(process.PID, syscall.SIGTERM)
		}
	}

	stopDeadline := time.Now().Add(5 * time.Second)
	for len(oldPIDs) > 0 && time.Now().Before(stopDeadline) && ctx.Err() == nil {
		for pid := range oldPIDs {
			if processSignalMissing(pid) {
				delete(oldPIDs, pid)
			}
		}
		if len(oldPIDs) > 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	for pid := range oldPIDs {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	forceDeadline := time.Now().Add(2 * time.Second)
	for len(oldPIDs) > 0 && time.Now().Before(forceDeadline) && ctx.Err() == nil {
		for pid := range oldPIDs {
			if processSignalMissing(pid) {
				delete(oldPIDs, pid)
			}
		}
		if len(oldPIDs) > 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	appPath := strings.TrimSpace(os.Getenv("WECHAT_APP"))
	if appPath == "" {
		appPath = "/Applications/WeChat.app"
	}
	if info, statErr := os.Stat(appPath); statErr != nil || !info.IsDir() {
		return 0, fmt.Errorf("WeChat app path is unavailable: %s", appPath)
	}
	if output, openErr := exec.CommandContext(ctx, "/usr/bin/open", "-a", appPath).CombinedOutput(); openErr != nil {
		return 0, fmt.Errorf("open WeChat: %w: %s", openErr, strings.TrimSpace(string(output)))
	}

	startDeadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(startDeadline) && ctx.Err() == nil {
		current, listErr := listRuntimeOSProcesses(ctx)
		if listErr == nil {
			for _, process := range current {
				if isWeChatMainProcess(process.Command) {
					return process.PID, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return 0, fmt.Errorf("WeChat restart did not expose a main process before timeout")
}

func isWeChatMainProcess(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	return strings.Contains(lower, "/wechat.app/contents/macos/wechat") &&
		!strings.Contains(lower, "wechatappex") &&
		!strings.Contains(lower, "/helpers/")
}

func exitCurrentChatlogProcess() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	// Headless mode consumes SIGTERM through signal.NotifyContext; if its main
	// return path is delayed, keep the dashboard action bounded.
	time.Sleep(1200 * time.Millisecond)
	os.Exit(0)
}
