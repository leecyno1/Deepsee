//go:build !darwin

package imgdownload

import (
	"context"
	"time"
)

// PrewarmFridaDaemon is only available for the macOS WeChat client.
func PrewarmFridaDaemon(ctx context.Context) error {
	_ = ctx
	return ErrNotConfigured
}

func PrewarmScopedFridaDaemon(ctx context.Context) error {
	_ = ctx
	return ErrNotConfigured
}

func SetScopedDownloadTalkers(ctx context.Context, talkers []string) error {
	_ = ctx
	_ = talkers
	return ErrNotConfigured
}

func AllowScopedDownloadFileIDs(ctx context.Context, fileIDs []string, ttl time.Duration) error {
	_ = ctx
	_ = fileIDs
	_ = ttl
	return ErrNotConfigured
}

// StopFridaDaemon is a no-op outside darwin.
func StopFridaDaemon() {}

// DaemonStatus reports that the resident helper is unavailable outside darwin.
func DaemonStatus(ctx context.Context) map[string]any {
	_ = ctx
	return map[string]any{"running": false, "available": false}
}
