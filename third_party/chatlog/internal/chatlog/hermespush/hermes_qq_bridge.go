package hermespush

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

//go:embed hermes_qq_bridge.py
var hermesQQBridgeScript string

type hermesQQBridgePayload struct {
	AppID        string   `json:"app_id"`
	ClientSecret string   `json:"client_secret"`
	ChatID       string   `json:"chat_id"`
	Text         string   `json:"text"`
	MediaPaths   []string `json:"media_paths"`
	HermesHome   string   `json:"hermes_home"`
	HermesRoot   string   `json:"hermes_root"`
}

func sendQQViaHermesPython(ctx context.Context, cfg *QQConfig, req QQSendRequest) error {
	pythonBin, hermesRoot, err := discoverHermesPython()
	if err != nil {
		return err
	}
	scriptPath, cleanup, err := ensureHermesQQBridgeScript()
	if err != nil {
		return err
	}
	defer cleanup()

	payload := hermesQQBridgePayload{
		AppID:        strings.TrimSpace(cfg.AppID),
		ClientSecret: strings.TrimSpace(cfg.ClientSecret),
		ChatID:       strings.TrimSpace(cfg.HomeChannel),
		Text:         strings.TrimSpace(req.Text),
		MediaPaths:   compactMediaPaths(req.MediaPaths),
		HermesHome:   strings.TrimSpace(cfg.HermesHome),
		HermesRoot:   hermesRoot,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, pythonBin, scriptPath)
	cmd.Env = append(os.Environ(), "HERMES_HOME="+payload.HermesHome)
	cmd.Stdin = bytes.NewReader(body)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("Hermes QQ send failed: %s", msg)
	}
	var result hermesBridgeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return fmt.Errorf("Hermes QQ send invalid response: %w", err)
	}
	if !result.Success {
		if strings.TrimSpace(result.Error) == "" {
			result.Error = "unknown error"
		}
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

func ensureHermesQQBridgeScript() (string, func(), error) {
	return materializeHermesBridgeAsset("hermes-qq-bridge", hermesQQBridgeScript)
}
