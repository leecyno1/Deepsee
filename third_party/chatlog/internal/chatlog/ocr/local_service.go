package ocr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sjzar/chatlog/pkg/util"
)

const (
	localServiceStateNotRequired = "not_required"
	localServiceStateStopped     = "stopped"
	localServiceStateStarting    = "starting"
	localServiceStateReady       = "ready"
	localServiceStateError       = "error"
	localServiceReadyProbeEvery  = 5 * time.Second
)

// LocalServiceSnapshot is safe to expose through the runtime status endpoint.
// It contains process lifecycle information but no environment values or keys.
type LocalServiceSnapshot struct {
	Managed       bool   `json:"managed"`
	AutoStart     bool   `json:"auto_start"`
	AutoRestart   bool   `json:"auto_restart"`
	ManualStopped bool   `json:"manual_stopped"`
	State         string `json:"state"`
	Message       string `json:"message,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	HealthURL     string `json:"health_url,omitempty"`
	RuntimeDir    string `json:"runtime_dir,omitempty"`
	LogDir        string `json:"log_dir,omitempty"`
	StartScript   string `json:"start_script,omitempty"`
	StopScript    string `json:"stop_script,omitempty"`
	BackendPID    int    `json:"backend_pid,omitempty"`
	SDKPID        int    `json:"sdk_pid,omitempty"`
	BackendReady  bool   `json:"backend_ready"`
	SDKReady      bool   `json:"sdk_ready"`
	Synchronized  bool   `json:"synchronized"`
	StartedAt     int64  `json:"started_at,omitempty"`
	LastCheckAt   int64  `json:"last_check_at,omitempty"`
	Error         string `json:"error,omitempty"`
}

type localServiceStateFile struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	UpdatedAt int64  `json:"updated_at"`
}

// LocalServiceManager starts the official two-process GLM-OCR stack on Apple
// Silicon: mlx-vlm on port 8080 and the GLM-OCR SDK server on port 8081.
type LocalServiceManager struct {
	endpoint    string
	healthURL   string
	backendAddr string
	runtimeDir  string
	startScript string
	stopScript  string
	autoStart   bool
	autoRestart bool
	managed     bool

	mu            sync.RWMutex
	state         string
	message       string
	startedAt     int64
	lastCheckAt   int64
	backendReady  bool
	sdkReady      bool
	lastError     string
	starting      bool
	startCancel   context.CancelFunc
	startSequence uint64
	everReady     bool
	manualStopped bool
}

type LocalServiceOptions struct {
	AutoStart   bool
	AutoRestart bool
}

func NewLocalServiceManager(endpoint string, options ...LocalServiceOptions) *LocalServiceManager {
	endpoint = strings.TrimSpace(endpoint)
	autoStart := localServiceAutoStartEnabled()
	autoRestart := localServiceAutoRestartEnabled()
	if len(options) > 0 {
		autoStart = options[0].AutoStart
		autoRestart = options[0].AutoRestart
	}
	manager := &LocalServiceManager{
		endpoint:    endpoint,
		healthURL:   localServiceHealthURL(endpoint),
		backendAddr: "127.0.0.1:8080",
		runtimeDir:  localServiceRuntimeDir(),
		autoStart:   autoStart,
		autoRestart: autoRestart,
		state:       localServiceStateNotRequired,
	}
	manager.managed = localServicePlatformSupported() && isManagedOCREndpoint(endpoint)
	if manager.managed {
		manager.state = localServiceStateStopped
		manager.startScript = findLocalServiceStartScript()
		manager.stopScript = findLocalServiceStopScript()
	}
	return manager
}

func (m *LocalServiceManager) Managed() bool {
	return m != nil && m.managed
}

func (m *LocalServiceManager) EnsureAsync() {
	if m == nil || !m.managed {
		return
	}
	m.mu.RLock()
	alreadyStarting := m.starting
	allowed := m.autoStart && !m.manualStopped && (!m.everReady || m.autoRestart)
	m.mu.RUnlock()
	if !allowed {
		return
	}
	if alreadyStarting {
		m.refreshStateFile()
		m.mu.Lock()
		alreadyStarting = localServiceProgressState(m.state)
		m.starting = alreadyStarting
		m.mu.Unlock()
		if alreadyStarting {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	ready := m.probe(ctx)
	cancel()
	if ready {
		m.setReady()
		return
	}
	m.startAsync(false)
}

func (m *LocalServiceManager) startAsync(manual bool) {
	m.mu.Lock()
	if manual {
		m.manualStopped = false
	}
	if m.starting {
		m.mu.Unlock()
		return
	}
	if strings.TrimSpace(m.startScript) == "" {
		m.state = localServiceStateError
		m.lastError = "未找到本地 OCR 自动启动脚本"
		m.message = m.lastError
		m.mu.Unlock()
		return
	}
	m.startSequence++
	sequence := m.startSequence
	startContext, startCancel := context.WithCancel(context.Background())
	m.startCancel = startCancel
	m.starting = true
	m.state = localServiceStateStarting
	m.message = "正在准备本地 OCR 服务"
	m.lastError = ""
	m.startedAt = time.Now().Unix()
	m.mu.Unlock()

	go func() {
		defer startCancel()
		m.runStartScript(startContext, sequence)
	}()
}

func (m *LocalServiceManager) Start() error {
	if m == nil || !m.managed {
		return errors.New("当前地址不由本机 OCR 进程托管")
	}
	m.startAsync(true)
	return nil
}

// SetAutoRestart updates the live supervisor policy. The HTTP stop action uses
// it together with persisted configuration so both the current process and the
// next Chatlog start keep the user's explicit stop decision.
func (m *LocalServiceManager) SetAutoRestart(enabled bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.autoRestart = enabled
	m.mu.Unlock()
}

// WorkerProbeEnabled reports whether a queued OCR task should keep checking a
// managed model startup. Explicitly stopped or auto-start-disabled services
// sleep until the start endpoint wakes the worker.
func (m *LocalServiceManager) WorkerProbeEnabled() bool {
	if m == nil || !m.managed {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.starting || m.state == localServiceStateReady {
		return true
	}
	return m.autoStart && !m.manualStopped && (!m.everReady || m.autoRestart)
}

func (m *LocalServiceManager) Stop(ctx context.Context) error {
	if m == nil || !m.managed {
		return errors.New("当前地址不由本机 OCR 进程托管")
	}
	m.mu.Lock()
	m.manualStopped = true
	m.starting = false
	m.startSequence++
	startCancel := m.startCancel
	m.startCancel = nil
	m.mu.Unlock()
	if startCancel != nil {
		startCancel()
	}
	if strings.TrimSpace(m.stopScript) == "" {
		return errors.New("未找到本地 OCR 停止脚本")
	}
	command := exec.CommandContext(ctx, m.stopScript)
	command.Env = append(os.Environ(), "CHATLOG_OCR_RUNTIME_DIR="+m.runtimeDir)
	output, err := command.CombinedOutput()
	message := strings.TrimSpace(string(output))
	backendReady, sdkReady := m.pairReady(ctx)
	m.mu.Lock()
	m.state = localServiceStateStopped
	m.message = "MLX 与 GLM-OCR SDK 已同步停止"
	m.lastError = ""
	if err != nil {
		m.state = localServiceStateError
		m.lastError = strings.TrimSpace(message + ": " + err.Error())
		m.message = "停止本地 OCR 服务失败"
	} else if backendReady || sdkReady {
		err = fmt.Errorf("model process pair is still running: mlx=%t sdk=%t", backendReady, sdkReady)
		m.state = localServiceStateError
		m.lastError = err.Error()
		m.message = "MLX 与 GLM-OCR SDK 未完成同步停止"
	}
	m.mu.Unlock()
	return err
}

func (m *LocalServiceManager) Ready(ctx context.Context) bool {
	if m == nil || !m.managed {
		return true
	}
	m.mu.RLock()
	recentlyReady := m.state == localServiceStateReady &&
		m.lastCheckAt > 0 &&
		time.Now().Unix()-m.lastCheckAt < int64(localServiceReadyProbeEvery/time.Second)
	m.mu.RUnlock()
	if recentlyReady {
		return true
	}
	backendReady, sdkReady := m.pairReady(ctx)
	if backendReady && sdkReady {
		m.setReady()
		return true
	}
	m.mu.RLock()
	starting := m.starting
	m.mu.RUnlock()
	if starting {
		return false
	}
	m.refreshStateFile()
	m.mu.Lock()
	if backendReady != sdkReady {
		m.state = localServiceStateError
		m.message = "MLX 与 GLM-OCR SDK 运行状态不同步"
		m.lastError = fmt.Sprintf("local OCR process pair diverged: mlx=%t sdk=%t", backendReady, sdkReady)
	} else if m.state == localServiceStateReady {
		m.state = localServiceStateStopped
		m.message = "MLX 与 GLM-OCR SDK 均未运行"
	}
	m.mu.Unlock()
	return false
}

func (m *LocalServiceManager) Snapshot(ctx context.Context) LocalServiceSnapshot {
	if m == nil {
		return LocalServiceSnapshot{
			State:   localServiceStateNotRequired,
			Message: "当前模式不需要本地 OCR 服务",
		}
	}
	if m.managed {
		_ = m.Ready(ctx)
	}
	m.mu.RLock()
	snapshot := LocalServiceSnapshot{
		Managed:       m.managed,
		AutoStart:     m.autoStart,
		AutoRestart:   m.autoRestart,
		ManualStopped: m.manualStopped,
		State:         m.state,
		Message:       m.message,
		Endpoint:      m.endpoint,
		HealthURL:     m.healthURL,
		RuntimeDir:    m.runtimeDir,
		LogDir:        filepath.Join(m.runtimeDir, "logs"),
		StartScript:   m.startScript,
		StopScript:    m.stopScript,
		StartedAt:     m.startedAt,
		LastCheckAt:   m.lastCheckAt,
		Error:         m.lastError,
	}
	m.mu.RUnlock()
	snapshot.BackendPID = readLocalServicePID(filepath.Join(m.runtimeDir, "run", "mlx.pid"))
	snapshot.SDKPID = readLocalServicePID(filepath.Join(m.runtimeDir, "run", "sdk.pid"))
	m.mu.RLock()
	snapshot.BackendReady = m.backendReady
	snapshot.SDKReady = m.sdkReady
	m.mu.RUnlock()
	snapshot.Synchronized = snapshot.BackendReady == snapshot.SDKReady
	return snapshot
}

func (m *LocalServiceManager) runStartScript(ctx context.Context, sequence uint64) {
	command := exec.CommandContext(ctx, m.startScript)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = 3 * time.Second
	command.Env = append(os.Environ(), "CHATLOG_OCR_RUNTIME_DIR="+m.runtimeDir)
	output, err := command.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if len(message) > 2000 {
		message = message[len(message)-2000:]
	}

	m.mu.Lock()
	if sequence != m.startSequence {
		m.mu.Unlock()
		return
	}
	m.starting = false
	m.startCancel = nil
	m.mu.Unlock()
	if err != nil {
		m.refreshStateFile()
		m.mu.Lock()
		if m.state != localServiceStateError {
			m.state = localServiceStateError
			m.message = "本地 OCR 服务启动失败"
		}
		m.lastError = strings.TrimSpace(strings.Join([]string{m.lastError, message, err.Error()}, ": "))
		if len(m.lastError) > 2000 {
			m.lastError = m.lastError[len(m.lastError)-2000:]
		}
		m.mu.Unlock()
		return
	}
	probeContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	backendReady, sdkReady := m.pairReady(probeContext)
	if backendReady && sdkReady {
		m.setReady()
		return
	}
	m.refreshStateFile()
	m.mu.Lock()
	m.state = localServiceStateError
	m.message = "MLX 与 GLM-OCR SDK 未完成同步启动"
	m.lastError = fmt.Sprintf("local OCR process pair did not become ready: mlx=%t sdk=%t", backendReady, sdkReady)
	m.mu.Unlock()
}

func (m *LocalServiceManager) probe(ctx context.Context) bool {
	backendReady, sdkReady := m.pairReady(ctx)
	return backendReady && sdkReady
}

func (m *LocalServiceManager) pairReady(ctx context.Context) (bool, bool) {
	if m == nil {
		return false, false
	}
	backendPID := readLocalServicePID(filepath.Join(m.runtimeDir, "run", "mlx.pid"))
	sdkPID := readLocalServicePID(filepath.Join(m.runtimeDir, "run", "sdk.pid"))
	backendReady := localServiceProcessAlive(backendPID) && localServicePortReady(m.backendAddr)
	sdkReady := localServiceProcessAlive(sdkPID) && localServiceHTTPReady(ctx, m.healthURL)
	m.mu.Lock()
	m.lastCheckAt = time.Now().Unix()
	m.backendReady = backendReady
	m.sdkReady = sdkReady
	m.mu.Unlock()
	return backendReady, sdkReady
}

func localServiceProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func localServiceHTTPReady(ctx context.Context, healthURL string) bool {
	if strings.TrimSpace(healthURL) == "" {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 700 * time.Millisecond}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func (m *LocalServiceManager) setReady() {
	m.mu.Lock()
	m.state = localServiceStateReady
	m.message = "MLX 与 GLM-OCR SDK 已同步运行"
	m.lastError = ""
	m.starting = false
	m.everReady = true
	m.manualStopped = false
	m.mu.Unlock()
}

func (m *LocalServiceManager) refreshStateFile() {
	if m == nil || strings.TrimSpace(m.runtimeDir) == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(m.runtimeDir, "run", "status.json"))
	if err != nil {
		return
	}
	var state localServiceStateFile
	if json.Unmarshal(data, &state) != nil {
		return
	}
	state.State = strings.TrimSpace(state.State)
	state.Message = strings.TrimSpace(state.Message)
	m.mu.Lock()
	if state.State != "" {
		m.state = state.State
	}
	if state.Message != "" {
		m.message = state.Message
	}
	if state.State == localServiceStateError {
		m.lastError = state.Message
	}
	m.mu.Unlock()
}

func localServiceHealthURL(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/health"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func isLoopbackOCREndpoint(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func isManagedOCREndpoint(endpoint string) bool {
	if !isLoopbackOCREndpoint(endpoint) {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	return parsed.Port() == "8081"
}

func localServiceAutoStartEnabled() bool {
	value, ok := os.LookupEnv("CHATLOG_OCR_LOCAL_AUTO_START")
	if !ok {
		return true
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && enabled
}

func localServiceAutoRestartEnabled() bool {
	value, ok := os.LookupEnv("CHATLOG_OCR_LOCAL_AUTO_RESTART")
	if !ok {
		return true
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && enabled
}

func localServiceRuntimeDir() string {
	if value := strings.TrimSpace(os.Getenv("CHATLOG_OCR_RUNTIME_DIR")); value != "" {
		return filepath.Clean(value)
	}
	return filepath.Join(util.AppRootDir(), "ocr-runtime")
}

func findLocalServiceStartScript() string {
	if value := strings.TrimSpace(os.Getenv("CHATLOG_OCR_LOCAL_START_SCRIPT")); value != "" {
		if executableFile(value) {
			return filepath.Clean(value)
		}
		return ""
	}
	if start, _, err := materializeEmbeddedLocalServiceAssets(); err == nil {
		return start
	}
	candidates := make([]string, 0, 4)
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "deploy", "ocr", "start-macos-mlx.sh"))
	}
	if _, sourceFile, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(
			filepath.Dir(sourceFile), "..", "..", "..", "deploy", "ocr", "start-macos-mlx.sh",
		))
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(executable), "deploy", "ocr", "start-macos-mlx.sh"),
			filepath.Join(filepath.Dir(executable), "start-macos-mlx.sh"),
		)
	}
	for _, candidate := range candidates {
		if executableFile(candidate) {
			absolute, err := filepath.Abs(candidate)
			if err == nil {
				return absolute
			}
		}
	}
	return ""
}

func findLocalServiceStopScript() string {
	if value := strings.TrimSpace(os.Getenv("CHATLOG_OCR_LOCAL_STOP_SCRIPT")); value != "" {
		if executableFile(value) {
			return filepath.Clean(value)
		}
		return ""
	}
	if startOverride := strings.TrimSpace(os.Getenv("CHATLOG_OCR_LOCAL_START_SCRIPT")); startOverride != "" {
		candidate := filepath.Join(filepath.Dir(filepath.Clean(startOverride)), "stop-macos-mlx.sh")
		if executableFile(candidate) {
			return candidate
		}
	}
	if _, stop, err := materializeEmbeddedLocalServiceAssets(); err == nil {
		return stop
	}
	if start := findLocalServiceStartScript(); start != "" {
		candidate := filepath.Join(filepath.Dir(start), "stop-macos-mlx.sh")
		if executableFile(candidate) {
			return candidate
		}
	}
	return ""
}

func readLocalServicePID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func localServicePortReady(address string) bool {
	connection, err := net.DialTimeout("tcp", address, 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func executableFile(path string) bool {
	info, err := os.Stat(filepath.Clean(strings.TrimSpace(path)))
	return err == nil && !info.IsDir() && info.Mode().Perm()&0100 != 0
}

func localServiceProgressState(state string) bool {
	switch strings.TrimSpace(state) {
	case "installing", "installed", localServiceStateStarting, "starting_backend",
		"warming_backend", "starting_sdk", "warming":
		return true
	default:
		return false
	}
}

func (s LocalServiceSnapshot) Validate() error {
	if s.Managed && strings.TrimSpace(s.Endpoint) == "" {
		return errors.New("managed local OCR service requires an endpoint")
	}
	if s.State == "" {
		return fmt.Errorf("local OCR service state is empty")
	}
	return nil
}
