package ports

import "context"

// KeyCaptureState is an opaque, typed handle owned by a capture adapter.
type KeyCaptureState interface {
	IsKeyCaptureState()
}

// DataKeyCapture carries an application-visible key plus adapter-owned capture
// state. The use case can pass it back without depending on platform types.
type DataKeyCapture struct {
	Value string
	State KeyCaptureState
}

type KeyCapture interface {
	Available() bool
	IsImagePermissionError(err error) bool
	ExtractImageKey(ctx context.Context, pid uint32, dataDir string, status func(string)) (string, error)
	CaptureDataKeys(ctx context.Context, dataDir string, status func(string)) (DataKeyCapture, error)
	ApplyDataKeys(dataDir string, capture DataKeyCapture, status func(string)) error
}
