package darwin

import (
	"context"
	"errors"
	"fmt"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	keydarwin "github.com/sjzar/chatlog/internal/wechat/key/darwin"
)

type KeyCapture struct{}

type captureState []keydarwin.CapturedDBKey

func (captureState) IsKeyCaptureState() {}

func NewKeyCapture() *KeyCapture {
	return &KeyCapture{}
}

func (*KeyCapture) Available() bool {
	return keydarwin.FridaAvailable()
}

func (*KeyCapture) IsImagePermissionError(err error) bool {
	return errors.Is(err, keydarwin.ErrImageKeyPermission)
}

func (*KeyCapture) ExtractImageKey(
	ctx context.Context,
	pid uint32,
	dataDir string,
	status func(string),
) (string, error) {
	return keydarwin.ExtractImageKeyWithAuthorization(ctx, pid, dataDir, status)
}

func (*KeyCapture) CaptureDataKeys(
	ctx context.Context,
	dataDir string,
	status func(string),
) (ports.DataKeyCapture, error) {
	dataKey, candidates, err := keydarwin.ExtractKeysViaFrida(ctx, dataDir, status)
	if err != nil {
		return ports.DataKeyCapture{}, err
	}
	return ports.DataKeyCapture{Value: dataKey, State: captureState(candidates)}, nil
}

func (*KeyCapture) ApplyDataKeys(dataDir string, capture ports.DataKeyCapture, status func(string)) error {
	candidates, ok := capture.State.(captureState)
	if !ok {
		return fmt.Errorf("unexpected key capture state %T", capture.State)
	}
	_, _, err := keydarwin.ApplyCapturedKeysToDataDir(dataDir, []keydarwin.CapturedDBKey(candidates), status)
	return err
}

var _ ports.KeyCapture = (*KeyCapture)(nil)
