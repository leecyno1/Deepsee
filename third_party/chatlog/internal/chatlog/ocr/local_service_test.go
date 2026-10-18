package ocr

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbeddedLocalServiceAssetsMaterializeOnce(t *testing.T) {
	t.Setenv("CHATLOG_DIR", t.TempDir())
	start, stop, err := materializeEmbeddedLocalServiceAssets()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(start) != filepath.Dir(stop) {
		t.Fatalf("start and stop assets use different directories: %q %q", start, stop)
	}
	firstInfo, err := os.Stat(start)
	if err != nil {
		t.Fatal(err)
	}
	if firstInfo.Mode().Perm()&0o100 == 0 {
		t.Fatalf("start script is not executable: %s", firstInfo.Mode())
	}
	firstMTime := firstInfo.ModTime()
	time.Sleep(10 * time.Millisecond)
	againStart, againStop, err := materializeEmbeddedLocalServiceAssets()
	if err != nil {
		t.Fatal(err)
	}
	if againStart != start || againStop != stop {
		t.Fatalf("materialized paths changed: (%q, %q) -> (%q, %q)", start, stop, againStart, againStop)
	}
	secondInfo, err := os.Stat(start)
	if err != nil {
		t.Fatal(err)
	}
	if !secondInfo.ModTime().Equal(firstMTime) {
		t.Fatalf("unchanged OCR assets were rewritten: %s -> %s", firstMTime, secondInfo.ModTime())
	}
}

func TestEmbeddedLocalServiceAssetsMatchDeploymentCopies(t *testing.T) {
	for _, asset := range localServiceAssetManifest {
		embedded, err := embeddedLocalServiceAssets.ReadFile(filepath.ToSlash(filepath.Join("assets", asset.name)))
		if err != nil {
			t.Fatal(err)
		}
		deployment, err := os.ReadFile(filepath.Join("../../../deploy/ocr", asset.name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(embedded, deployment) {
			t.Fatalf("embedded OCR asset %s is out of sync with deploy/ocr", asset.name)
		}
	}
}

func TestEmbeddedLocalServiceAssetsSynchronizeProcessPair(t *testing.T) {
	start, err := embeddedLocalServiceAssets.ReadFile("assets/start-macos-mlx.sh")
	if err != nil {
		t.Fatal(err)
	}
	stop, err := embeddedLocalServiceAssets.ReadFile("assets/stop-macos-mlx.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range [][]byte{
		[]byte("pid_alive \"$RUN_DIR/mlx.pid\" mlx && pid_alive \"$RUN_DIR/sdk.pid\" sdk && backend_healthy && sdk_healthy"),
		[]byte("trap cleanup_start EXIT"),
		[]byte("stop_pair"),
		[]byte("MLX 与 GLM-OCR SDK 已同步运行"),
	} {
		if !bytes.Contains(start, expected) {
			t.Errorf("start script is missing %q", expected)
		}
	}
	for _, expected := range [][]byte{
		[]byte("kill -TERM $pids"),
		[]byte("append_descendants"),
		[]byte("MLX 与 GLM-OCR SDK 已同步停止"),
	} {
		if !bytes.Contains(stop, expected) {
			t.Errorf("stop script is missing %q", expected)
		}
	}
}

func TestLocalServiceRuntimeDirFollowsChatlogRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHATLOG_DIR", root)
	t.Setenv("CHATLOG_OCR_RUNTIME_DIR", "")
	if got, want := localServiceRuntimeDir(), filepath.Join(root, "ocr-runtime"); got != want {
		t.Fatalf("runtime dir = %q, want %q", got, want)
	}
}

func TestLocalServiceCustomStartUsesSiblingStop(t *testing.T) {
	dir := t.TempDir()
	start := filepath.Join(dir, "custom-start.sh")
	stop := filepath.Join(dir, "stop-macos-mlx.sh")
	if err := os.WriteFile(start, []byte("#!/bin/zsh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stop, []byte("#!/bin/zsh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATLOG_OCR_LOCAL_START_SCRIPT", start)
	t.Setenv("CHATLOG_OCR_LOCAL_STOP_SCRIPT", "")
	if got := findLocalServiceStartScript(); got != start {
		t.Fatalf("start script = %q, want %q", got, start)
	}
	if got := findLocalServiceStopScript(); got != stop {
		t.Fatalf("stop script = %q, want %q", got, stop)
	}
}

func TestLocalServiceEndpointDetection(t *testing.T) {
	if !isLoopbackOCREndpoint("http://127.0.0.1:8081/glmocr/parse") {
		t.Fatal("IPv4 loopback endpoint was not detected")
	}
	if !isLoopbackOCREndpoint("http://localhost:8081/glmocr/parse") {
		t.Fatal("localhost endpoint was not detected")
	}
	if isLoopbackOCREndpoint("https://ocr.example.com/glmocr/parse") {
		t.Fatal("remote endpoint was treated as managed")
	}
	if !isManagedOCREndpoint("http://127.0.0.1:8081/glmocr/parse") {
		t.Fatal("default local SDK endpoint was not detected as managed")
	}
	if isManagedOCREndpoint("http://127.0.0.1:9000/glmocr/parse") {
		t.Fatal("custom local endpoint was treated as the managed SDK")
	}
	if got := localServiceHealthURL("http://127.0.0.1:8081/glmocr/parse?x=1"); got != "http://127.0.0.1:8081/health" {
		t.Fatalf("health URL = %q", got)
	}
}

func TestLocalServiceManagerThrottlesHealthyProbes(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	runtimeDir := writeTestLocalServicePIDs(t)

	manager := &LocalServiceManager{
		managed:      true,
		healthURL:    server.URL,
		backendAddr:  backend.Addr().String(),
		runtimeDir:   runtimeDir,
		state:        localServiceStateReady,
		lastCheckAt:  time.Now().Unix(),
		backendReady: true,
		sdkReady:     true,
	}
	if !manager.Ready(context.Background()) || requests.Load() != 0 {
		t.Fatalf("fresh ready state performed %d health probes", requests.Load())
	}
	manager.mu.Lock()
	manager.lastCheckAt = time.Now().Add(-localServiceReadyProbeEvery).Unix()
	manager.mu.Unlock()
	if !manager.Ready(context.Background()) || requests.Load() != 1 {
		t.Fatalf("stale ready state performed %d health probes", requests.Load())
	}
}

func TestLocalServiceManagerRequiresSynchronizedPair(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	runtimeDir := writeTestLocalServicePIDs(t)
	manager := &LocalServiceManager{
		managed:     true,
		healthURL:   server.URL,
		backendAddr: backend.Addr().String(),
		runtimeDir:  runtimeDir,
		state:       localServiceStateStopped,
	}
	if !manager.Ready(context.Background()) {
		t.Fatal("healthy MLX and SDK pair was not ready")
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.lastCheckAt = 0
	manager.mu.Unlock()
	if manager.Ready(context.Background()) {
		t.Fatal("SDK-only split state was reported ready")
	}
	snapshot := manager.Snapshot(context.Background())
	if snapshot.Synchronized || snapshot.BackendReady || !snapshot.SDKReady {
		t.Fatalf("split snapshot = %#v", snapshot)
	}
	if snapshot.State != localServiceStateError {
		t.Fatalf("split state = %q, want error", snapshot.State)
	}
}

func writeTestLocalServicePIDs(t *testing.T) string {
	t.Helper()
	runtimeDir := t.TempDir()
	runDir := filepath.Join(runtimeDir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pid := []byte(fmt.Sprintf("%d\n", os.Getpid()))
	for _, name := range []string{"mlx.pid", "sdk.pid"} {
		if err := os.WriteFile(filepath.Join(runDir, name), pid, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runtimeDir
}

func TestLocalServiceManualStopCancelsInFlightPairStart(t *testing.T) {
	dir := t.TempDir()
	start := filepath.Join(dir, "start.sh")
	stop := filepath.Join(dir, "stop.sh")
	if err := os.WriteFile(start, []byte("#!/bin/zsh\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stop, []byte("#!/bin/zsh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := &LocalServiceManager{
		managed:     true,
		healthURL:   "http://127.0.0.1:1/health",
		backendAddr: "127.0.0.1:1",
		runtimeDir:  dir,
		startScript: start,
		stopScript:  stop,
		state:       localServiceStateStopped,
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	snapshot := manager.Snapshot(context.Background())
	if snapshot.State != localServiceStateStopped || !snapshot.ManualStopped || !snapshot.Synchronized {
		t.Fatalf("stopped snapshot = %#v", snapshot)
	}
}

func TestLocalServiceManagerDetectsHealthyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/health" {
			http.NotFound(writer, request)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager := NewLocalServiceManager(server.URL + "/glmocr/parse")
	if manager.Managed() {
		t.Fatal("custom local service should remain externally managed")
	}
	if !manager.Ready(context.Background()) {
		t.Fatal("external local service should not block the OCR worker")
	}
	snapshot := manager.Snapshot(context.Background())
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalServiceManagerCanDisableAutoRestartImmediately(t *testing.T) {
	manager := NewLocalServiceManager(
		"http://127.0.0.1:8081/glmocr/parse",
		LocalServiceOptions{AutoStart: true, AutoRestart: true},
	)
	manager.SetAutoRestart(false)
	snapshot := manager.Snapshot(context.Background())
	if snapshot.AutoRestart {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestLocalServiceWorkerProbeHonorsManualPolicy(t *testing.T) {
	manager := NewLocalServiceManager(
		"http://127.0.0.1:8081/glmocr/parse",
		LocalServiceOptions{AutoStart: false, AutoRestart: false},
	)
	if manager.WorkerProbeEnabled() {
		t.Fatal("auto-start-disabled manager enabled worker probes")
	}
	manager.mu.Lock()
	manager.starting = true
	manager.mu.Unlock()
	if !manager.WorkerProbeEnabled() {
		t.Fatal("manual startup did not enable worker readiness probes")
	}
	manager.mu.Lock()
	manager.starting = false
	manager.manualStopped = true
	manager.mu.Unlock()
	if manager.WorkerProbeEnabled() {
		t.Fatal("manual stop left worker readiness probes enabled")
	}
}
