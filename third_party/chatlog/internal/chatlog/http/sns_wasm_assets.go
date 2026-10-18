package http

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/sjzar/chatlog/pkg/util"
)

const maxSNSWasmKeystreamSize = 8 << 20

//go:embed wasm/wasm_video_decode.js wasm/wasm_video_decode.wasm wasm/wasm_keystream_helper.js
var snsWasmAssets embed.FS

var (
	snsWasmInitMu  sync.Mutex
	snsWasmBaseDir string
)

func (s *Service) getSNSWasmKeystream(ctx context.Context, key string, size int, mode string) ([]byte, error) {
	if size <= 0 || size > maxSNSWasmKeystreamSize {
		return nil, fmt.Errorf("sns wasm keystream size %d is outside 1..%d", size, maxSNSWasmKeystreamSize)
	}
	baseDir, err := s.ensureSNSWasmAssets()
	if err != nil {
		return nil, err
	}
	if mode == "" {
		mode = "reversed"
	}

	cmd := exec.CommandContext(ctx, "node", filepath.Join(baseDir, "wasm_keystream_helper.js"), key, strconv.Itoa(size), mode)
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	maxOutput := int64(base64.StdEncoding.EncodedLen(size) + 16)
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxOutput+1))
	if int64(len(out)) > maxOutput {
		_ = stdout.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil, fmt.Errorf("sns wasm helper output exceeds %d bytes", maxOutput)
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return nil, fmt.Errorf("sns wasm helper failed: %s", stderr.String())
	}
	if readErr != nil {
		return nil, readErr
	}

	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(out)))
	n, err := base64.StdEncoding.Decode(decoded, out)
	if err != nil {
		return nil, err
	}
	decoded = decoded[:n]
	if len(decoded) != size {
		return nil, fmt.Errorf("sns wasm helper returned %d bytes, want %d", len(decoded), size)
	}
	return decoded, nil
}

func (s *Service) ensureSNSWasmAssets() (string, error) {
	snsWasmInitMu.Lock()
	defer snsWasmInitMu.Unlock()

	if snsWasmBaseDir != "" {
		return snsWasmBaseDir, nil
	}

	baseDir := util.CacheDirAt(s.conf.GetRuntimeDir(), "sns_wasm")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return "", err
	}

	files := []string{
		"wasm_video_decode.js",
		"wasm_video_decode.wasm",
		"wasm_keystream_helper.js",
	}
	for _, name := range files {
		data, err := snsWasmAssets.ReadFile("wasm/" + name)
		if err != nil {
			return "", err
		}
		target := filepath.Join(baseDir, name)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return "", err
		}
	}

	snsWasmBaseDir = baseDir
	return snsWasmBaseDir, nil
}
