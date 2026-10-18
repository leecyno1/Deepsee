package http

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const unknownMessageSampleFileName = "unknown_message_formats.json"

func classifyAuditMessagePayload(raw []byte, decoded string, decodeErr error) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "empty"
	}
	if bytes.HasPrefix(raw, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		if decodeErr != nil {
			return "zstd_error"
		}
		return "zstd"
	}
	value := strings.TrimSpace(decoded)
	switch {
	case strings.HasPrefix(value, "<"):
		return "xml"
	case strings.HasPrefix(value, "{"), strings.HasPrefix(value, "["):
		return "json"
	case utf8.Valid(raw):
		return "text"
	default:
		return "binary"
	}
}

func (s *Service) persistUnknownMessageSamples(report *databaseAuditReport) {
	if s == nil || s.conf == nil || report == nil {
		return
	}
	root := strings.TrimSpace(s.conf.GetRuntimeDir())
	if root == "" {
		return
	}
	dir := filepath.Join(root, "diagnostics")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	target := filepath.Join(dir, unknownMessageSampleFileName)
	payload := struct {
		GeneratedAt string                       `json:"generated_at"`
		Samples     []databaseAuditUnknownSample `json:"samples"`
	}{
		GeneratedAt: report.GeneratedAt,
		Samples:     append([]databaseAuditUnknownSample(nil), report.Messages.UnknownSamples...),
	}
	file, err := os.CreateTemp(dir, unknownMessageSampleFileName+".*.tmp")
	if err != nil {
		return
	}
	tmpPath := file.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		_ = file.Close()
		return
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return
	}
	if err := file.Close(); err != nil {
		return
	}
	if err := replaceAuditSampleFile(tmpPath, target); err != nil {
		return
	}
	report.Messages.UnknownSampleFile = target
}

func replaceAuditSampleFile(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, target)
}
