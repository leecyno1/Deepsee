package filemonitor

import (
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func TestHandleEventRunsCallbacksWithoutSpawningPerEventGoroutines(t *testing.T) {
	dir := t.TempDir()
	group, err := NewFileGroup("db", dir, `.*\.db$`, nil)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	group.AddCallback(func(fsnotify.Event) error {
		called++
		return nil
	})

	group.HandleEvent(fsnotify.Event{
		Name: filepath.Join(dir, "message.db"),
		Op:   fsnotify.Write,
	})
	if called != 1 {
		t.Fatalf("callback count = %d, want 1", called)
	}
}
