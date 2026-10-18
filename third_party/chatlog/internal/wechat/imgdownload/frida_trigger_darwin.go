//go:build darwin

package imgdownload

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sjzar/chatlog/pkg/util"
)

// FridaTrigger manages the receive hook that asks WeChat's own live CDN request
// to use the normal-image tier. For an already indexed thumbnail, the daemon
// can also submit a minimal self-contained request on WeChat's main queue.
//
// Experimental: requires matching wechat.dylib build and SIP disabled for attach.
type FridaTrigger struct {
	Python            string
	ScriptPath        string
	WeChatPID         int // 0 = auto pgrep
	Mode              string
	PrimeWait         time.Duration
	AllowHashMismatch bool
	// UpgradeReceive enables the process-wide passive variant 3 -> 2 hook.
	// Keep this false for targeted downloads: the passive hook has no talker
	// identity and would otherwise upgrade images from unrelated conversations.
	UpgradeReceive bool
	// ScopedReceive asks the passive hook to upgrade only CDN file ids that the
	// OCR scanner registers through the daemon allowlist.
	ScopedReceive bool
}

var (
	//go:embed assets/wechat_image_download_frida.py
	embeddedImageDownloadFridaScript []byte

	defaultScriptCandidates = []string{
		"scripts/wechat_image_download_frida.py",
	}
	fridaOnce   sync.Once
	fridaPython string
	fridaOK     bool
)

// FridaAvailable reports whether python3 + frida can be used.
func FridaAvailable() bool {
	fridaOnce.Do(func() {
		for _, py := range []string{"python3", "python"} {
			cmd := exec.Command(py, "-c", "import frida; print(frida.__version__)")
			out, err := cmd.CombinedOutput()
			if err == nil && strings.TrimSpace(string(out)) != "" {
				fridaPython = py
				fridaOK = true
				return
			}
		}
	})
	return fridaOK
}

// NewFridaTrigger returns a trigger using the bundled Frida helper script.
// scriptPath may be empty to search relative to the working directory / executable.
func NewFridaTrigger(scriptPath string) *FridaTrigger {
	return &FridaTrigger{
		Python:     "",
		ScriptPath: scriptPath,
		Mode:       "auto",
		// Wait briefly for passive CDN manager capture (thumb traffic etc.).
		PrimeWait: 1500 * time.Millisecond,
	}
}

// NewScopedFridaTrigger returns a trigger that keeps the resident receive hook
// in allowlist mode while actively requesting a registered image.
func NewScopedFridaTrigger(scriptPath string) *FridaTrigger {
	t := NewFridaTrigger(scriptPath)
	t.ScopedReceive = true
	return t
}

func (t *FridaTrigger) resolvePython() string {
	if t != nil && strings.TrimSpace(t.Python) != "" {
		return t.Python
	}
	if FridaAvailable() {
		return fridaPython
	}
	return "python3"
}

func (t *FridaTrigger) resolveScript() (string, error) {
	if t != nil && strings.TrimSpace(t.ScriptPath) != "" {
		if _, err := os.Stat(t.ScriptPath); err == nil {
			return t.ScriptPath, nil
		}
		return "", fmt.Errorf("frida script not found: %s", t.ScriptPath)
	}
	// The release binary carries the verified helper. Materialize it once at a
	// content-addressed cache path so launches from Finder, make run, or any cwd
	// all use the same complete resource set without repeated writes.
	if path, err := materializeEmbeddedImageDownloadFridaScript(); err == nil {
		return path, nil
	}
	// Prefer repo-relative path from cwd.
	for _, rel := range defaultScriptCandidates {
		if abs, err := filepath.Abs(rel); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs, nil
			}
		}
	}
	// Next to executable (release layouts).
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, name := range []string{
			filepath.Join(dir, "wechat_image_download_frida.py"),
			filepath.Join(dir, "scripts", "wechat_image_download_frida.py"),
		} {
			if _, err := os.Stat(name); err == nil {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("wechat_image_download_frida.py not found")
}

func materializeEmbeddedImageDownloadFridaScript() (string, error) {
	if len(embeddedImageDownloadFridaScript) == 0 {
		return "", fmt.Errorf("embedded image download frida script is empty")
	}
	digest := sha256.Sum256(embeddedImageDownloadFridaScript)
	dir := util.DefaultCacheDir("runtime_assets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("wechat_image_download_frida-%x.py", digest[:8]))
	if content, err := os.ReadFile(path); err == nil && bytes.Equal(content, embeddedImageDownloadFridaScript) {
		_ = os.Chmod(path, 0o700)
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, ".wechat_image_download_frida-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o700); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(embeddedImageDownloadFridaScript); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	return path, nil
}

type fridaInvokeResult struct {
	OK    bool           `json:"ok"`
	Error string         `json:"error"`
	Wait  map[string]any `json:"wait"`
}

// RequestDownload implements Trigger.
//
// By default uses a process-wide resident Frida daemon (long-lived attach) so
// the CDN manager stays primed after any WeChat CDN traffic. Set
// CHATLOG_FRIDA_ONESHOT=1 to force the legacy attach-per-request path.
func (t *FridaTrigger) RequestDownload(ctx context.Context, req Request) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !FridaAvailable() {
		return fmt.Errorf("%w: frida unavailable", ErrNotConfigured)
	}
	if !req.CDN.Usable() {
		return fmt.Errorf("%w: missing aeskey/cdn file id for media_key=%s", ErrNotConfigured, req.MediaKey)
	}
	if !ActiveReplayEnabled() {
		// Production only arms the scoped live-request hook. Keeping WeChat's
		// original request preserves its callback, hardlink index and encrypted
		// cache lifecycle.
		return sharedFridaDaemon(t).daemonArmDownload(ctx, t, req)
	}
	if strings.TrimSpace(os.Getenv("CHATLOG_FRIDA_ONESHOT")) == "1" {
		return t.requestDownloadOneshot(ctx, req)
	}
	return sharedFridaDaemon(t).daemonDownload(ctx, t, req)
}

// ActiveReplayEnabled reports whether research-only synthetic replay was
// explicitly selected. Live request mutation is the production default.
func ActiveReplayEnabled() bool {
	return strings.TrimSpace(os.Getenv("CHATLOG_FRIDA_ACTIVE_INVOKE")) == "1"
}

// requestDownloadOneshot is the legacy attach → invoke → detach path.
func (t *FridaTrigger) requestDownloadOneshot(ctx context.Context, req Request) error {
	script, err := t.resolveScript()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	py := t.resolvePython()
	mode := strings.TrimSpace(t.Mode)
	if mode == "" {
		mode = "auto"
	}
	args := []string{
		script,
		"--file-id", req.CDN.PreferFileID(),
		"--aes-key", req.CDN.AesKey,
		"--request-key", nativeRequestKey(req),
		"--media-key", req.MediaKey,
		"--media-path", nativeDownloadPath(req),
		"--media-length", req.CDN.Length,
		"--mode", mode,
		"--json",
	}
	if t.WeChatPID > 0 {
		args = append(args, "--pid", fmt.Sprintf("%d", t.WeChatPID))
	}
	if t.PrimeWait > 0 {
		args = append(args, "--prime-seconds", fmt.Sprintf("%.3f", t.PrimeWait.Seconds()))
	}
	if t.AllowHashMismatch || strings.TrimSpace(os.Getenv("CHATLOG_FRIDA_ALLOW_HASH_MISMATCH")) == "1" {
		args = append(args, "--allow-hash-mismatch")
	}
	args = append(args, "--active-invoke")
	waitFile := strings.TrimSpace(req.WaitFile)
	if waitFile != "" {
		timeout := req.Timeout
		if timeout <= 0 {
			timeout = 45 * time.Second
		}
		args = append(args,
			"--wait-file", waitFile,
			"--wait-timeout", fmt.Sprintf("%.0f", timeout.Seconds()),
			"--min-size", "8000",
		)
	}

	// Hard ceiling so OCR never stays on "triggering" indefinitely when Frida hangs.
	invokeTimeout := 16 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 && rem < invokeTimeout {
			invokeTimeout = rem
		}
	}
	invokeCtx, cancel := context.WithTimeout(ctx, invokeTimeout)
	defer cancel()

	cmd := exec.Command(py, args...)
	// Own process group so we can kill Frida helper + children on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("image download frida start failed: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runErr error
	select {
	case runErr = <-done:
	case <-invokeCtx.Done():
		killFridaProcessGroup(cmd)
		// Prefer a short wait so Wait() reaps the process; do not block OCR.
		select {
		case runErr = <-done:
		case <-time.After(2 * time.Second):
			runErr = invokeCtx.Err()
		}
		if runErr == nil {
			runErr = invokeCtx.Err()
		}
	}

	combined := append(append([]byte{}, stdout.Bytes()...), stderr.Bytes()...)
	res, parseErr := parseFridaJSON(combined)
	if human := humanizeFridaError(res, parseErr, combined); human != "" && (runErr != nil || !res.OK) {
		if invokeCtx.Err() != nil {
			return fmt.Errorf("原图下载超时（%s）：%s", invokeTimeout.Round(time.Second), human)
		}
		return fmt.Errorf("%s", human)
	}
	if runErr != nil {
		if parseErr == nil && res.OK {
			return nil
		}
		if invokeCtx.Err() != nil {
			return fmt.Errorf("原图下载超时（%s）：Frida 未在时限内返回", invokeTimeout.Round(time.Second))
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = runErr.Error()
		}
		return fmt.Errorf("原图下载触发失败：%s", truncate(msg, 240))
	}
	if parseErr != nil {
		return fmt.Errorf("原图下载结果解析失败：%s", truncate(stdout.String(), 240))
	}
	if !res.OK {
		if res.Error != "" {
			return fmt.Errorf("%s", humanizeFridaError(res, nil, nil))
		}
		return fmt.Errorf("原图下载触发失败：invoke not ok")
	}
	return nil
}

func killFridaProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// Negative PID = process group (requires Setpgid).
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

var fridaErrorField = regexp.MustCompile(`"error"\s*:\s*"((?:\\.|[^"\\])*)"`)

func parseFridaJSON(raw []byte) (fridaInvokeResult, error) {
	raw = bytes.TrimSpace(raw)
	var res fridaInvokeResult
	if len(raw) == 0 {
		return res, fmt.Errorf("empty output")
	}
	// Prefer the last JSON object in the stream (Frida may print noise first).
	if err := json.Unmarshal(raw, &res); err == nil {
		return res, nil
	}
	lines := bytes.Split(raw, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if err := json.Unmarshal(line, &res); err == nil {
			return res, nil
		}
	}
	// Last resort: extract a JSON object substring.
	if start := bytes.LastIndexByte(raw, '{'); start >= 0 {
		if end := bytes.LastIndexByte(raw, '}'); end > start {
			if err := json.Unmarshal(raw[start:end+1], &res); err == nil {
				return res, nil
			}
		}
	}
	return res, fmt.Errorf("no json object in frida output")
}

func humanizeFridaError(res fridaInvokeResult, parseErr error, raw []byte) string {
	errText := strings.TrimSpace(res.Error)
	if errText == "" && parseErr != nil && len(raw) > 0 {
		if m := fridaErrorField.FindSubmatch(raw); len(m) > 1 {
			errText = string(m[1])
			errText = strings.ReplaceAll(errText, `\"`, `"`)
			errText = strings.ReplaceAll(errText, `\n`, " ")
		}
	}
	if errText == "" {
		return ""
	}
	lower := strings.ToLower(errText)
	switch {
	case strings.Contains(lower, "cdn_manager_unavailable") || strings.Contains(lower, "no_manager") || strings.Contains(lower, "not primed"):
		return "微信 CDN 管理器尚未就绪：常驻 Frida 将继续监听后续图片接收活动"
	case strings.Contains(lower, "hash mismatch"):
		return "wechat.dylib 版本与内置偏移不匹配，自动下载不可用（可设置 CHATLOG_FRIDA_ALLOW_HASH_MISMATCH=1 强制尝试）"
	case strings.Contains(lower, "wechat not running"):
		return "微信未运行，无法触发原图下载"
	case strings.Contains(lower, "frida not installed"):
		return "本机未安装 Frida（python3 -c 'import frida'）"
	case strings.Contains(lower, "timed out"):
		return "Frida 调用超时：" + truncate(errText, 160)
	default:
		return "原图下载失败：" + truncate(errText, 200)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
