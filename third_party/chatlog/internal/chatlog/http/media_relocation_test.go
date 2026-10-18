package http

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sjzar/chatlog/internal/model"
)

type mediaRelocationConfig struct {
	lifecycleConfig
	dataDir string
}

func (config mediaRelocationConfig) GetDataDir() string { return config.dataDir }

func TestFindRelocatedMediaFileUsesCurrentAttachmentDirectory(t *testing.T) {
	dataDir := t.TempDir()
	expected := filepath.Join(dataDir, "msg", "attach", "new-session", "2026-07", "Img", "image_h.dat")
	if err := os.MkdirAll(filepath.Dir(expected), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expected, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{conf: mediaRelocationConfig{dataDir: dataDir}}
	got := service.findRelocatedMediaFile(&model.Media{
		Type: "image",
		Name: "image_h.dat",
		Path: "msg/attach/stale-session/2025-01/Img/image_h.dat",
	})
	if got != expected {
		t.Fatalf("relocated path = %q, want %q", got, expected)
	}
}
