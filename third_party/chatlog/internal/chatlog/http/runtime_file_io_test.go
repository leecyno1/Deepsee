package http

import (
	"os"
	"strings"
	"testing"
)

func TestSelectRuntimeFileIOProcessesIncludesRelatedTrees(t *testing.T) {
	processes := []runtimeOSProcess{
		{PID: 10, PPID: 1, Command: "/tmp/chatlog-alpha ops server-start"},
		{PID: 11, PPID: 10, Command: "python3 child_without_marker.py"},
		{PID: 12, PPID: 11, Command: "resource_tracker"},
		{PID: 20, PPID: 1, Command: "python3 -m glmocr.server --config /tmp/config.yaml"},
		{PID: 21, PPID: 20, Command: "python3 worker.py"},
		{PID: 22, PPID: 10, Command: "/bin/ps -axo pid=,ppid=,command="},
		{PID: 23, PPID: 10, Command: "/usr/bin/fs_usage -w -f filesys 10 20"},
		{PID: 30, PPID: 1, Command: "java -jar /Users/test/.local/share/gitflic-runner/chatlog-alpha/runner.jar"},
	}

	selected := selectRuntimeFileIOProcesses(processes, 10)
	selectedPIDs := make(map[int]struct{}, len(selected))
	for _, process := range selected {
		selectedPIDs[process.PID] = struct{}{}
	}
	for _, expected := range []int{10, 11, 12, 20, 21} {
		if _, exists := selectedPIDs[expected]; !exists {
			t.Errorf("pid %d was not selected: %+v", expected, selected)
		}
	}
	if _, exists := selectedPIDs[30]; exists {
		t.Fatalf("unrelated GitFlic runner was selected: %+v", selected)
	}
	if _, exists := selectedPIDs[22]; exists {
		t.Fatalf("the monitor's transient ps helper was selected: %+v", selected)
	}
	if _, exists := selectedPIDs[23]; exists {
		t.Fatalf("the monitor's fs_usage helper was selected: %+v", selected)
	}
}

func TestRuntimeFileIODeltaHandlesRestartedCounters(t *testing.T) {
	if delta := runtimeFileIODelta(100, 140, true); delta != 40 {
		t.Fatalf("monotonic delta = %d, want 40", delta)
	}
	if delta := runtimeFileIODelta(100, 8, true); delta != 8 {
		t.Fatalf("reset delta = %d, want 8", delta)
	}
	if delta := runtimeFileIODelta(100, 140, false); delta != 140 {
		t.Fatalf("new process delta = %d, want 140", delta)
	}
}

func TestSanitizeRuntimeProcessCommandRedactsSecrets(t *testing.T) {
	command := sanitizeRuntimeProcessCommand("chatlog --data-key fixture-key --api-key=fixture-api --account wxid_fixture")
	if strings.Contains(command, "fixture-key") || strings.Contains(command, "fixture-api") {
		t.Fatalf("sensitive command flags were retained: %q", command)
	}
	if !strings.Contains(command, "--data-key ***") || !strings.Contains(command, "--api-key=***") {
		t.Fatalf("redaction markers missing: %q", command)
	}
}

func TestCurrentProcessFileIOCountersAreReadable(t *testing.T) {
	readBytes, writeBytes, err := readRuntimeProcessFileIO(os.Getpid())
	if err != nil {
		t.Fatalf("read current process file I/O: %v", err)
	}
	if readBytes == 0 && writeBytes == 0 {
		t.Log("current test process has not issued physical disk I/O yet")
	}
}
