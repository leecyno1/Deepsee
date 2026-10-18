//go:build darwin

package imgdownload

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Default daemon socket lives under TMPDIR so multiple users do not collide.
func defaultFridaDaemonSocket() string {
	if p := strings.TrimSpace(os.Getenv("CHATLOG_FRIDA_IMGDL_SOCKET")); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "chatlog-wechat-imgdl.sock")
}

// fridaDaemon is a process-wide resident Frida helper (one attach to WeChat).
type fridaDaemon struct {
	mu         sync.Mutex
	python     string
	script     string
	socketPath string
	cmd        *exec.Cmd
	allowHash  bool
	startedAt  time.Time
}

var (
	daemonOnce sync.Once
	daemonInst *fridaDaemon
)

func sharedFridaDaemon(t *FridaTrigger) *fridaDaemon {
	daemonOnce.Do(func() {
		daemonInst = &fridaDaemon{
			socketPath: defaultFridaDaemonSocket(),
		}
	})
	d := daemonInst
	if t != nil {
		if py := strings.TrimSpace(t.Python); py != "" {
			d.python = py
		}
		if sp := strings.TrimSpace(t.ScriptPath); sp != "" {
			d.script = sp
		}
		if t.AllowHashMismatch || strings.TrimSpace(os.Getenv("CHATLOG_FRIDA_ALLOW_HASH_MISMATCH")) == "1" {
			d.allowHash = true
		}
	}
	return d
}

func (d *fridaDaemon) resolvePython() string {
	if d != nil && strings.TrimSpace(d.python) != "" {
		return d.python
	}
	if FridaAvailable() {
		return fridaPython
	}
	return "python3"
}

func (d *fridaDaemon) resolveScript(t *FridaTrigger) (string, error) {
	if d != nil && strings.TrimSpace(d.script) != "" {
		if _, err := os.Stat(d.script); err == nil {
			return d.script, nil
		}
	}
	if t != nil {
		return t.resolveScript()
	}
	tmp := &FridaTrigger{}
	return tmp.resolveScript()
}

func fridaDaemonArgs(script, socketPath string, parentPID int, t *FridaTrigger, allowHash bool) []string {
	args := []string{
		script,
		"--daemon",
		"--parent-pid", fmt.Sprintf("%d", parentPID),
		"--socket", socketPath,
		"--mode", "auto",
		"--attach-timeout", "8",
		"--invoke-timeout", "8",
	}
	if ActiveReplayEnabled() {
		args = append(args, "--active-invoke")
	}
	if t != nil && t.UpgradeReceive {
		args = append(args, "--upgrade-receive")
	}
	if t != nil && t.ScopedReceive {
		args = append(args, "--scoped-receive")
	}
	if allowHash {
		args = append(args, "--allow-hash-mismatch")
	}
	if t != nil && t.WeChatPID > 0 {
		args = append(args, "--pid", fmt.Sprintf("%d", t.WeChatPID))
	}
	return args
}

// daemonArmDownload refreshes the file-id allowlist without calling a private
// WeChat function. The next real receive/open request is upgraded in-place on
// WeChat's own thread.
func (d *fridaDaemon) daemonArmDownload(ctx context.Context, t *FridaTrigger, req Request) error {
	if err := d.ensure(ctx, t); err != nil {
		return err
	}
	fileID := strings.TrimSpace(req.CDN.PreferFileID())
	if fileID == "" {
		return fmt.Errorf("%w: missing CDN file id for media_key=%s", ErrNotConfigured, req.MediaKey)
	}
	ttl := req.Timeout
	if ttl < 2*time.Minute {
		ttl = 2 * time.Minute
	}
	resp, err := d.request(ctx, map[string]any{
		"cmd":         "allow_files",
		"file_ids":    []string{fileID},
		"ttl_seconds": ttl.Seconds(),
	}, 2*time.Second)
	if err != nil {
		return err
	}
	ok, _ := resp["ok"].(bool)
	if !ok {
		return fmt.Errorf("register scoped image download: daemon rejected request")
	}
	return nil
}

func (d *fridaDaemon) alive() bool {
	if d == nil || strings.TrimSpace(d.socketPath) == "" {
		return false
	}
	// Probe the socket rather than only the child tracked by this Go process.
	// A previous Chatlog instance may already own the singleton daemon, and a
	// second process must reuse it instead of unlinking its socket and creating a
	// second Frida session against WeChat.
	resp, err := d.requestLocked(context.Background(), map[string]any{"cmd": "ping"}, 500*time.Millisecond)
	if err != nil {
		return false
	}
	ok, _ := resp["ok"].(bool)
	return ok
}

func (d *fridaDaemon) ensure(ctx context.Context, t *FridaTrigger) error {
	d.mu.Lock()
	if d.alive() {
		if t != nil {
			resp, err := d.requestLocked(ctx, map[string]any{
				"cmd":     "set_upgrade",
				"enabled": t.UpgradeReceive,
			}, 2*time.Second)
			ok, _ := resp["ok"].(bool)
			if err != nil || !ok {
				d.mu.Unlock()
				if err != nil {
					return fmt.Errorf("configure frida receive upgrade: %w", err)
				}
				return fmt.Errorf("configure frida receive upgrade: daemon rejected request")
			}
			resp, err = d.requestLocked(ctx, map[string]any{
				"cmd":     "set_scoped",
				"enabled": t.ScopedReceive,
			}, 2*time.Second)
			ok, _ = resp["ok"].(bool)
			if err != nil || !ok {
				d.mu.Unlock()
				if err != nil {
					return fmt.Errorf("configure frida scoped receive: %w", err)
				}
				return fmt.Errorf("configure frida scoped receive: daemon rejected request")
			}
		}
		d.mu.Unlock()
		return nil
	}
	// Clean stale socket / dead process.
	d.stopLocked()

	script, err := d.resolveScript(t)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	py := d.resolvePython()
	args := fridaDaemonArgs(script, d.socketPath, os.Getpid(), t, d.allowHash)

	cmd := exec.Command(py, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Keep stderr for debug when CHATLOG_FRIDA_DAEMON_LOG=1.
	if strings.TrimSpace(os.Getenv("CHATLOG_FRIDA_DAEMON_LOG")) == "1" {
		cmd.Stderr = os.Stderr
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("frida daemon stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("frida daemon start: %w", err)
	}
	d.cmd = cmd
	d.startedAt = time.Now()
	socketPath := d.socketPath
	d.mu.Unlock()

	// Wait for daemon_ready line or socket without holding the mutex so status
	// polls and concurrent callers are not blocked for the full attach window.
	ready := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var msg map[string]any
			if json.Unmarshal([]byte(line), &msg) == nil {
				if msg["event"] == "daemon_ready" || msg["ok"] == true {
					ready <- nil
					go func() {
						for sc.Scan() {
						}
					}()
					return
				}
			}
		}
		if err := sc.Err(); err != nil {
			ready <- err
			return
		}
		ready <- fmt.Errorf("daemon exited before ready")
	}()

	timeout := 12 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 && rem < timeout {
			timeout = rem
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-ready:
		if err != nil {
			d.mu.Lock()
			d.stopLocked()
			d.mu.Unlock()
			return fmt.Errorf("frida daemon ready: %w", err)
		}
	case <-timer.C:
		if !waitSocket(socketPath, 2*time.Second) {
			d.mu.Lock()
			d.stopLocked()
			d.mu.Unlock()
			return fmt.Errorf("frida daemon ready timed out")
		}
	case <-ctx.Done():
		d.mu.Lock()
		d.stopLocked()
		d.mu.Unlock()
		return ctx.Err()
	}

	if !waitSocket(socketPath, 2*time.Second) {
		d.mu.Lock()
		d.stopLocked()
		d.mu.Unlock()
		return fmt.Errorf("frida daemon socket missing: %s", socketPath)
	}
	go pruneFridaCacheAsync()
	return nil
}

func waitSocket(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func (d *fridaDaemon) stopLocked() {
	if d.cmd != nil && d.cmd.Process != nil {
		_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_, _ = d.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(1500 * time.Millisecond):
			_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
			_, _ = d.cmd.Process.Wait()
		}
	}
	d.cmd = nil
	if d.socketPath != "" {
		_ = os.Remove(d.socketPath)
	}
}

// StopFridaDaemon terminates the process-wide resident helper (best-effort).
func StopFridaDaemon() {
	if daemonInst == nil {
		return
	}
	daemonInst.mu.Lock()
	defer daemonInst.mu.Unlock()
	// Prefer graceful shutdown over the socket.
	_, _ = daemonInst.requestLocked(context.Background(), map[string]any{"cmd": "shutdown"}, 2*time.Second)
	daemonInst.stopLocked()
}

func (d *fridaDaemon) request(ctx context.Context, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requestLocked(ctx, payload, timeout)
}

func (d *fridaDaemon) requestLocked(ctx context.Context, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 && rem < timeout {
			timeout = rem
		}
	}
	dialTimeout := 2 * time.Second
	if timeout < dialTimeout {
		dialTimeout = timeout
	}
	conn, err := net.DialTimeout("unix", d.socketPath, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("frida daemon dial: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if _, err := conn.Write(raw); err != nil {
		return nil, fmt.Errorf("frida daemon write: %w", err)
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("frida daemon read: %w", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		return nil, fmt.Errorf("frida daemon parse: %w; out=%s", err, truncate(string(line), 240))
	}
	return resp, nil
}

// daemonDownload talks to the resident Frida helper.
func (d *fridaDaemon) daemonDownload(ctx context.Context, t *FridaTrigger, req Request) error {
	if err := d.ensure(ctx, t); err != nil {
		return err
	}
	mode := "auto"
	if t != nil && strings.TrimSpace(t.Mode) != "" {
		mode = strings.TrimSpace(t.Mode)
	}
	waitManager := 2.0
	if t != nil && t.PrimeWait > 0 {
		waitManager = t.PrimeWait.Seconds()
	}
	payload := map[string]any{
		"cmd":                  "download",
		"file_id":              req.CDN.PreferFileID(),
		"aes_key":              req.CDN.AesKey,
		"request_key":          nativeRequestKey(req),
		"media_key":            req.MediaKey,
		"media_path":           nativeDownloadPath(req),
		"media_length":         req.CDN.Length,
		"mode":                 mode,
		"wait_manager_seconds": waitManager,
	}
	if wf := strings.TrimSpace(req.WaitFile); wf != "" {
		timeout := req.Timeout
		if timeout <= 0 {
			timeout = 45 * time.Second
		}
		payload["wait_file"] = wf
		payload["wait_timeout"] = timeout.Seconds()
		payload["min_size"] = 8000
	}

	// Invoke ceiling: attach already done; RPC should return quickly.
	timeout := 12 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 && rem < timeout {
			timeout = rem
		}
	}
	resp, err := d.request(ctx, payload, timeout)
	if err != nil {
		// One restart retry.
		d.mu.Lock()
		d.stopLocked()
		d.mu.Unlock()
		if err2 := d.ensure(ctx, t); err2 != nil {
			return fmt.Errorf("frida daemon: %v (restart: %v)", err, err2)
		}
		resp, err = d.request(ctx, payload, timeout)
		if err != nil {
			return err
		}
	}

	ok, _ := resp["ok"].(bool)
	if ok {
		return nil
	}
	// Normalize into fridaInvokeResult for humanize.
	errText, _ := resp["error"].(string)
	if errText == "" {
		if inv, ok := resp["invoke"].(map[string]any); ok {
			if e, ok := inv["error"].(string); ok {
				errText = e
			}
		}
	}
	res := fridaInvokeResult{OK: false, Error: errText}
	raw, _ := json.Marshal(resp)
	if human := humanizeFridaError(res, nil, raw); human != "" {
		return fmt.Errorf("%s", human)
	}
	if errText != "" {
		return fmt.Errorf("原图下载失败：%s", truncate(errText, 200))
	}
	return fmt.Errorf("原图下载失败：daemon invoke not ok")
}

// PrewarmFridaDaemon starts the resident helper before the first image arrives.
// Attaching early lets the hook upgrade WeChat's live incoming request from
// thumbnail variant 3 to normal-image variant 2 in the original receive thread.
func PrewarmFridaDaemon(ctx context.Context) error {
	trigger := NewFridaTrigger("")
	trigger.UpgradeReceive = true
	return prewarmFridaDaemon(ctx, trigger)
}

// PrewarmScopedFridaDaemon starts the resident helper in allowlist mode. The
// hook remains passive for requests outside the preconfigured talker scope and
// for requests whose CDN file id was not explicitly registered.
func PrewarmScopedFridaDaemon(ctx context.Context) error {
	trigger := NewFridaTrigger("")
	trigger.ScopedReceive = true
	return prewarmFridaDaemon(ctx, trigger)
}

func prewarmFridaDaemon(ctx context.Context, trigger *FridaTrigger) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !FridaAvailable() {
		return fmt.Errorf("%w: frida unavailable", ErrNotConfigured)
	}
	d := sharedFridaDaemon(trigger)
	if err := d.ensure(ctx, trigger); err != nil {
		return err
	}

	// The daemon can become ready before its best-effort initial attach has
	// completed (for example while WeChat is starting). Wait briefly for the
	// attached status; its own watcher keeps retrying across WeChat restarts.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := DaemonStatus(ctx)
		if attached, _ := status["attached"].(bool); attached {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("frida daemon prewarm: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// SetScopedDownloadTalkers replaces the resident receive hook's talker scope.
// WeChat embeds the talker in the live request before it writes the message DB,
// so this path avoids the database-change race inherent in file-id-only scope.
func SetScopedDownloadTalkers(ctx context.Context, talkers []string) error {
	if daemonInst == nil {
		return fmt.Errorf("%w: scoped frida daemon is not running", ErrNotConfigured)
	}
	clean := make([]string, 0, len(talkers))
	seen := make(map[string]struct{}, len(talkers))
	for _, value := range talkers {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		clean = append(clean, value)
	}
	resp, err := daemonInst.request(ctx, map[string]any{
		"cmd":     "set_scope_talkers",
		"talkers": clean,
	}, 2*time.Second)
	if err != nil {
		return err
	}
	ok, _ := resp["ok"].(bool)
	if !ok {
		return fmt.Errorf("configure scoped image talkers: daemon rejected request")
	}
	return nil
}

// AllowScopedDownloadFileIDs registers CDN file ids for a short time. A live
// variant-3 request is upgraded only when its id appears in this allowlist.
func AllowScopedDownloadFileIDs(ctx context.Context, fileIDs []string, ttl time.Duration) error {
	if daemonInst == nil {
		return fmt.Errorf("%w: scoped frida daemon is not running", ErrNotConfigured)
	}
	clean := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, value := range fileIDs {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		clean = append(clean, value)
	}
	if len(clean) == 0 {
		return nil
	}
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	resp, err := daemonInst.request(ctx, map[string]any{
		"cmd":         "allow_files",
		"file_ids":    clean,
		"ttl_seconds": ttl.Seconds(),
	}, 2*time.Second)
	if err != nil {
		return err
	}
	ok, _ := resp["ok"].(bool)
	if !ok {
		return fmt.Errorf("register scoped image downloads: daemon rejected request")
	}
	return nil
}

// DaemonStatus returns resident helper status when running.
func DaemonStatus(ctx context.Context) map[string]any {
	if daemonInst == nil {
		return map[string]any{"running": false}
	}
	d := daemonInst
	// Non-blocking snapshot: do not wait on the start lock.
	d.mu.Lock()
	cmd := d.cmd
	socket := d.socketPath
	started := d.startedAt.Unix()
	trackedAlive := cmd != nil && cmd.Process != nil && cmd.Process.Signal(syscall.Signal(0)) == nil
	d.mu.Unlock()

	// Short dial without taking the daemon mutex for long — request() takes mu.
	// Use a direct dial so status stays snappy even during download.  A healthy
	// socket may belong to a singleton adopted from another Chatlog process.
	conn, err := net.DialTimeout("unix", socket, 250*time.Millisecond)
	if err != nil {
		status := map[string]any{"running": trackedAlive, "socket": socket}
		if trackedAlive {
			status["started_at"] = started
			status["error"] = err.Error()
		}
		return status
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(350 * time.Millisecond))
	payload, _ := json.Marshal(map[string]any{"cmd": "status"})
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		return map[string]any{"running": true, "socket": socket, "started_at": started, "error": err.Error()}
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return map[string]any{"running": true, "socket": socket, "started_at": started, "error": err.Error()}
	}
	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		return map[string]any{"running": true, "socket": socket, "started_at": started, "error": err.Error()}
	}
	resp["running"] = true
	resp["socket"] = socket
	if started > 0 {
		resp["started_at"] = started
	}
	_ = ctx
	return resp
}
