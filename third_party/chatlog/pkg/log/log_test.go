package log

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInitWritesAndCloseReleasesWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chatlog.log")
	w := Init(path, DefaultRetentionDays)
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
	if _, err := w.Write([]byte("after close\n")); !errors.Is(err, errClosed) {
		t.Fatalf("write after close error = %v, want %v", err, errClosed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat log: %v", err)
	}
}

func TestInitIsIdempotentAndCloseAllowsReinit(t *testing.T) {
	dir := t.TempDir()
	first := Init(filepath.Join(dir, "first.log"), 7)
	second := Init(filepath.Join(dir, "second.log"), 30)
	if first != second {
		t.Fatal("repeated Init returned a different writer")
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}

	third := Init(filepath.Join(dir, "third.log"), 30)
	defer Close()
	if third == first {
		t.Fatal("Init after Close reused the closed writer")
	}
	if _, err := third.Write([]byte("new writer\n")); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionNormalization(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, DefaultRetentionDays},
		{-1, DefaultRetentionDays},
		{1, 1},
		{30, 30},
		{365, 365},
		{500, MaxRetentionDays},
	}
	for _, tc := range tests {
		if got := NormalizeRetentionDays(tc.in); got != tc.want {
			t.Errorf("NormalizeRetentionDays(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestRotateCreatesBackupAndKeepsActiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chatlog.log")
	w := newWriter(path, 7)
	w.logger.Compress = false
	if _, err := w.Write([]byte("before rotation\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.rotate(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("after rotation\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}

	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(active), "after rotation") {
		t.Fatalf("active log content = %q", active)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "chatlog-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("rotated backups = %v, want one", backups)
	}
	rotated, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rotated), "before rotation") {
		t.Fatalf("rotated log content = %q", rotated)
	}
}

func TestSetRetentionIsSynchronizedWithWrites(t *testing.T) {
	defer Close()
	w := Init(filepath.Join(t.TempDir(), "chatlog.log"), 7)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = w.Write([]byte("line\n"))
				SetRetention(1 + j%MaxRetentionDays)
			}
		}()
	}
	wg.Wait()
}

func TestCleanupExpiredUsesCurrentRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chatlog.log")
	w := newWriter(path, 7)
	now := time.Now()
	old := filepath.Join(dir, "chatlog-2026-01-01T00-00-00.000.log.gz")
	recent := filepath.Join(dir, "chatlog-2026-08-02T00-00-00.000.log.gz")
	unrelated := filepath.Join(dir, "other-2026-01-01T00-00-00.000.log.gz")
	manual := filepath.Join(dir, "chatlog-important.log")
	for _, item := range []string{old, recent, unrelated, manual} {
		if err := os.WriteFile(item, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(old, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recent, now.Add(-6*24*time.Hour), now.Add(-6*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unrelated, now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(manual, now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	w.cleanupExpired(now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired backup still exists: %v", err)
	}
	for _, item := range []string{recent, unrelated, manual} {
		if _, err := os.Stat(item); err != nil {
			t.Fatalf("retained file %s: %v", item, err)
		}
	}
}

func TestNextMidnightUsesCalendarBoundary(t *testing.T) {
	location := time.FixedZone("test", 8*60*60)
	now := time.Date(2026, 8, 3, 23, 59, 59, 0, location)
	want := time.Date(2026, 8, 4, 0, 0, 0, 0, location)
	if got := nextMidnight(now); !got.Equal(want) {
		t.Fatalf("nextMidnight() = %v, want %v", got, want)
	}
}

func TestCloseWithoutInit(t *testing.T) {
	if err := Close(); err != nil {
		t.Fatal(err)
	}
}
