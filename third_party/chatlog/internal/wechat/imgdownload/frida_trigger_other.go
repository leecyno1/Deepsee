//go:build !darwin

package imgdownload

import "context"

// FridaAvailable is only supported on macOS WeChat.
func FridaAvailable() bool { return false }

// ActiveReplayEnabled is false where the macOS WeChat hook is unavailable.
func ActiveReplayEnabled() bool { return false }

// FridaTrigger is a stub on non-darwin platforms.
type FridaTrigger struct{}

// NewFridaTrigger returns a non-functional stub outside darwin.
func NewFridaTrigger(scriptPath string) *FridaTrigger {
	_ = scriptPath
	return &FridaTrigger{}
}

// NewScopedFridaTrigger mirrors the Darwin constructor for portable callers.
func NewScopedFridaTrigger(scriptPath string) *FridaTrigger {
	return NewFridaTrigger(scriptPath)
}

// RequestDownload always reports not configured outside darwin.
func (t *FridaTrigger) RequestDownload(ctx context.Context, req Request) error {
	_ = ctx
	_ = req
	return ErrNotConfigured
}
