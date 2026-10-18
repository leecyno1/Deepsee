//go:build darwin

package http

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type darwinRuntimeFileTraceSession struct {
	reader   *os.File
	dir      string
	stopPath string
	stopOnce sync.Once
}

func runtimeFileTracePlatformSupport() (bool, string) {
	if _, err := os.Stat("/usr/bin/fs_usage"); err != nil {
		return false, "系统未找到 fs_usage"
	}
	if _, err := os.Stat("/usr/bin/osascript"); err != nil {
		return false, "系统未找到 osascript"
	}
	return true, "macOS fs_usage 内核文件事件追踪可用"
}

func runtimeFileTraceOpenFiles(pids map[int]struct{}) map[int]map[int]runtimeFileTraceOpenFile {
	if len(pids) == 0 {
		return nil
	}
	values := make([]int, 0, len(pids))
	for pid := range pids {
		if pid > 0 {
			values = append(values, pid)
		}
	}
	values = sortedRuntimeFileTracePIDs(values)
	pidArgs := make([]string, 0, len(values))
	for _, pid := range values {
		pidArgs = append(pidArgs, strconv.Itoa(pid))
	}
	if len(pidArgs) == 0 {
		return nil
	}
	output, _ := exec.Command("/usr/sbin/lsof", "-n", "-P", "-a", "-p", strings.Join(pidArgs, ","), "-F", "pftn").Output()
	return parseRuntimeFileTraceOpenFiles(string(output))
}

func startRuntimeFileTracePlatform(pids []int) (runtimeFileTracePlatformSession, error) {
	pids = sortedRuntimeFileTracePIDs(pids)
	if len(pids) == 0 {
		return nil, fmt.Errorf("追踪目标进程为空")
	}
	dir, err := os.MkdirTemp("", "chatlog-file-trace-")
	if err != nil {
		return nil, fmt.Errorf("创建追踪运行目录: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	fifoPath := filepath.Join(dir, "events.pipe")
	stopPath := filepath.Join(dir, "stop")
	readyPath := filepath.Join(dir, "ready")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("创建追踪事件管道: %w", err)
	}

	// Keep one endpoint open until fs_usage is launched. This prevents the root
	// writer from blocking on FIFO open while osascript returns its wrapper PID.
	hold, err := os.OpenFile(fifoPath, os.O_RDWR, 0o600)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("打开追踪事件管道: %w", err)
	}

	pidArgs := make([]string, 0, len(pids))
	for _, pid := range pids {
		if pid > 0 {
			pidArgs = append(pidArgs, strconv.Itoa(pid))
		}
	}
	shellCommand := fmt.Sprintf(
		`( exec 3>%s || exit 1; : >%s; /usr/bin/fs_usage -w -f filesys %s >&3 2>&1 & child=$!; trap '/bin/kill -TERM "$child" >/dev/null 2>&1' EXIT INT TERM; while [ ! -e %s ] && /bin/kill -0 %d >/dev/null 2>&1; do /bin/sleep 1; done; /bin/kill -TERM "$child" >/dev/null 2>&1; wait "$child" 2>/dev/null ) >/dev/null 2>&1 & echo $!`,
		shellQuoteRuntimeFileTrace(fifoPath), shellQuoteRuntimeFileTrace(readyPath), strings.Join(pidArgs, " "), shellQuoteRuntimeFileTrace(stopPath), os.Getpid(),
	)
	appleScript := `do shell script "` + escapeAppleScriptRuntimeFileTrace(shellCommand) + `" with administrator privileges`
	output, launchErr := exec.Command("/usr/bin/osascript", "-e", appleScript).CombinedOutput()
	if launchErr != nil {
		_ = hold.Close()
		cleanup()
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = launchErr.Error()
		}
		return nil, fmt.Errorf("启动 fs_usage 详细追踪: %s", truncateRuntimeFileTraceText(detail, 400))
	}
	readyDeadline := time.Now().Add(5 * time.Second)
	for {
		if _, readyErr := os.Stat(readyPath); readyErr == nil {
			break
		}
		if time.Now().After(readyDeadline) {
			_ = os.WriteFile(stopPath, []byte("stop\n"), 0o600)
			_ = hold.Close()
			go delayedRuntimeFileTraceCleanup(dir)
			return nil, fmt.Errorf("启动 fs_usage 详细追踪: FIFO 写入端握手超时")
		}
		time.Sleep(20 * time.Millisecond)
	}

	reader, err := os.Open(fifoPath)
	_ = hold.Close()
	if err != nil {
		_ = os.WriteFile(stopPath, []byte("stop\n"), 0o600)
		go delayedRuntimeFileTraceCleanup(dir)
		return nil, fmt.Errorf("连接追踪事件管道: %w", err)
	}
	return &darwinRuntimeFileTraceSession{reader: reader, dir: dir, stopPath: stopPath}, nil
}

func (s *darwinRuntimeFileTraceSession) Read(buffer []byte) (int, error) {
	return s.reader.Read(buffer)
}

func (s *darwinRuntimeFileTraceSession) Close() error {
	s.Stop()
	return nil
}

func (s *darwinRuntimeFileTraceSession) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		_ = os.WriteFile(s.stopPath, []byte("stop\n"), 0o600)
		if s.reader != nil {
			_ = s.reader.Close()
		}
		go delayedRuntimeFileTraceCleanup(s.dir)
	})
}

func delayedRuntimeFileTraceCleanup(dir string) {
	// The privileged wrapper checks the stop marker once per second. Keep the
	// directory long enough for it to observe the marker before removing it.
	time.Sleep(3 * time.Second)
	_ = os.RemoveAll(dir)
}

func shellQuoteRuntimeFileTrace(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func escapeAppleScriptRuntimeFileTrace(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}
