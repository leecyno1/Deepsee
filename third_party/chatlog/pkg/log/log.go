// Package log provides the process log writer used by Chatlog.
//
// The active chatlog.log is rotated at local midnight and whenever it reaches
// MaxSizeMB. Rotated files are compressed and removed after the configured
// retention period.
package log

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	DefaultRetentionDays = 7
	MinRetentionDays     = 1
	MaxRetentionDays     = 365
	MaxSizeMB            = 50
	backupTimeFormat     = "2006-01-02T15-04-05.000"
)

var errClosed = errors.New("chatlog log writer is closed")

// writer serializes writes, rotation, retention changes, and shutdown. The
// outer mutex is required because lumberjack protects Write/Rotate internally,
// but its exported configuration fields are otherwise mutable without a lock.
type writer struct {
	mu            sync.Mutex
	logger        *lumberjack.Logger
	filename      string
	retentionDays int
	closed        bool
}

func newWriter(filename string, retentionDays int) *writer {
	return &writer{
		filename:      filename,
		retentionDays: NormalizeRetentionDays(retentionDays),
		logger: &lumberjack.Logger{
			Filename:   filename,
			MaxSize:    MaxSizeMB,
			MaxAge:     0, // retention is managed here so it can change without a data race
			MaxBackups: 0,
			LocalTime:  true,
			Compress:   true,
		},
	}
}

func (w *writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.logger == nil {
		return 0, errClosed
	}
	return w.logger.Write(p)
}

func (w *writer) setRetention(days int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed && w.logger != nil {
		w.retentionDays = NormalizeRetentionDays(days)
	}
}

func (w *writer) rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.logger == nil {
		return errClosed
	}
	return w.logger.Rotate()
}

func (w *writer) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.logger == nil {
		return nil
	}
	return w.logger.Close()
}

// cleanupExpired removes only lumberjack-style backups belonging to this log.
// It runs on startup and before each daily rotation. Keeping this policy out of
// lumberjack avoids mutating fields read by its asynchronous compression loop.
func (w *writer) cleanupExpired(now time.Time) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	filename := w.filename
	retentionDays := w.retentionDays
	w.mu.Unlock()

	dir := filepath.Dir(filename)
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "-"
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !isRotatedLog(entry.Name(), prefix, ext) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

func isRotatedLog(name, prefix, ext string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	base := strings.TrimSuffix(name, ".gz")
	if !strings.HasSuffix(base, ext) {
		return false
	}
	timestamp := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ext)
	_, err := time.Parse(backupTimeFormat, timestamp)
	return err == nil
}

var state struct {
	sync.Mutex
	writer *writer
	stop   chan struct{}
	done   chan struct{}
}

// Init initializes the singleton rotating writer. Repeated calls return the
// active writer; Close resets it so a later Init can start a fresh instance.
func Init(filename string, retentionDays int) io.Writer {
	state.Lock()
	defer state.Unlock()
	if state.writer != nil {
		return state.writer
	}

	active := newWriter(filename, retentionDays)
	stop := make(chan struct{})
	done := make(chan struct{})
	state.writer = active
	state.stop = stop
	state.done = done
	go runDailyRotation(active, stop, done)
	return active
}

// SetRetention updates the retention policy of the active writer. Values are
// normalized to the supported 1-365 day range.
func SetRetention(days int) {
	state.Lock()
	active := state.writer
	state.Unlock()
	if active != nil {
		active.setRetention(days)
	}
}

// Close stops the daily rotation loop and releases the log file descriptor.
func Close() error {
	state.Lock()
	active := state.writer
	stop := state.stop
	done := state.done
	state.writer = nil
	state.stop = nil
	state.done = nil
	if stop != nil {
		close(stop)
	}
	state.Unlock()

	if done != nil {
		<-done
	}
	if active == nil {
		return nil
	}
	return active.close()
}

// NormalizeRetentionDays converts omitted/invalid values to the default and
// clamps overly large values to the supported upper bound.
func NormalizeRetentionDays(days int) int {
	if days < MinRetentionDays {
		return DefaultRetentionDays
	}
	if days > MaxRetentionDays {
		return MaxRetentionDays
	}
	return days
}

func runDailyRotation(active *writer, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	active.cleanupExpired(time.Now())
	for {
		now := time.Now()
		timer := time.NewTimer(time.Until(nextMidnight(now)))
		select {
		case <-stop:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			active.cleanupExpired(time.Now())
			_ = active.rotate()
		}
	}
}

func nextMidnight(now time.Time) time.Time {
	year, month, day := now.Date()
	return time.Date(year, month, day+1, 0, 0, 0, 0, now.Location())
}
