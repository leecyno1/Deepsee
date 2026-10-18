//go:build darwin

package imgdownload

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFridaDaemonArgsUseLiveRequestMutationByDefault(t *testing.T) {
	t.Setenv("CHATLOG_FRIDA_ACTIVE_INVOKE", "")
	args := fridaDaemonArgs("helper.py", "/tmp/helper.sock", 42, &FridaTrigger{
		ScopedReceive: true,
		WeChatPID:     99,
	}, true)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--active-invoke") {
		t.Fatalf("production daemon unexpectedly enabled synthetic replay: %s", joined)
	}
	for _, expected := range []string{"--scoped-receive", "--allow-hash-mismatch", "--pid 99"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("daemon args missing %q: %s", expected, joined)
		}
	}
}

func TestFridaDaemonArgsEnableResearchReplayExplicitly(t *testing.T) {
	t.Setenv("CHATLOG_FRIDA_ACTIVE_INVOKE", "1")
	joined := strings.Join(fridaDaemonArgs("helper.py", "/tmp/helper.sock", 42, &FridaTrigger{}, false), " ")
	if !strings.Contains(joined, "--active-invoke") {
		t.Fatalf("explicit research replay flag missing: %s", joined)
	}
}

func TestFridaDaemonEnsureAdoptsHealthySocket(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("chatlog-imgdl-test-%d.sock", os.Getpid()))
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	})

	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer conn.Close()
		line, readErr := bufio.NewReader(conn).ReadBytes('\n')
		if readErr != nil {
			done <- readErr
			return
		}
		var request map[string]any
		if unmarshalErr := json.Unmarshal(line, &request); unmarshalErr != nil {
			done <- unmarshalErr
			return
		}
		if request["cmd"] != "ping" {
			done <- fmt.Errorf("unexpected command: %v", request["cmd"])
			return
		}
		_, writeErr := conn.Write([]byte("{\"ok\":true,\"cmd\":\"ping\",\"pid\":42}\n"))
		done <- writeErr
	}()

	d := &fridaDaemon{socketPath: socketPath}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.ensure(ctx, nil); err != nil {
		t.Fatalf("ensure should adopt healthy daemon socket: %v", err)
	}
	if d.cmd != nil {
		t.Fatal("adopted daemon should not spawn a second child process")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFridaDaemonEnsureDisablesUpgradeOnAdoptedSocket(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("chatlog-imgdl-mode-test-%d.sock", os.Getpid()))
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	})

	done := make(chan error, 1)
	go func() {
		for i, want := range []string{"ping", "set_upgrade", "set_scoped"} {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				done <- acceptErr
				return
			}
			line, readErr := bufio.NewReader(conn).ReadBytes('\n')
			if readErr != nil {
				_ = conn.Close()
				done <- readErr
				return
			}
			var request map[string]any
			if unmarshalErr := json.Unmarshal(line, &request); unmarshalErr != nil {
				_ = conn.Close()
				done <- unmarshalErr
				return
			}
			if request["cmd"] != want {
				_ = conn.Close()
				done <- fmt.Errorf("request %d command=%v want=%s", i, request["cmd"], want)
				return
			}
			if (want == "set_upgrade" || want == "set_scoped") && request["enabled"] != false {
				_ = conn.Close()
				done <- fmt.Errorf("set_upgrade enabled=%v", request["enabled"])
				return
			}
			_, writeErr := conn.Write([]byte("{\"ok\":true}\n"))
			_ = conn.Close()
			if writeErr != nil {
				done <- writeErr
				return
			}
		}
		done <- nil
	}()

	d := &fridaDaemon{socketPath: socketPath}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.ensure(ctx, &FridaTrigger{UpgradeReceive: false}); err != nil {
		t.Fatalf("ensure should adopt daemon and disable receive upgrade: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
