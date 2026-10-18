package usecase

import (
	"context"
	"testing"

	"github.com/sjzar/chatlog/internal/chatlog/ctx"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	iwechat "github.com/sjzar/chatlog/internal/wechat"
)

type keyServiceFixture struct {
	accounts []*iwechat.Account
}

type keyCaptureFixture struct{}

func (keyCaptureFixture) Available() bool { return true }
func (keyCaptureFixture) IsImagePermissionError(error) bool {
	return false
}
func (keyCaptureFixture) ExtractImageKey(context.Context, uint32, string, func(string)) (string, error) {
	return "", nil
}
func (keyCaptureFixture) CaptureDataKeys(context.Context, string, func(string)) (ports.DataKeyCapture, error) {
	return ports.DataKeyCapture{Value: "captured"}, nil
}
func (keyCaptureFixture) ApplyDataKeys(string, ports.DataKeyCapture, func(string)) error {
	return nil
}

type mediaKeysFixture struct {
	imageKey string
}

func (f *mediaKeysFixture) SetImageKey(key string) {
	f.imageKey = key
}

func (*mediaKeysFixture) RefreshXOR(string) (byte, error) {
	return 0, nil
}

func (f *keyServiceFixture) GetDataKey(account *iwechat.Account) (string, error) {
	account.Key = "fixture-data-key"
	return account.Key, nil
}

func (f *keyServiceFixture) GetImageKeyWithStatus(account *iwechat.Account, _ func(string)) (string, error) {
	account.ImgKey = "fixture-image-key"
	return account.ImgKey, nil
}

func (f *keyServiceFixture) GetWeChatInstances() []*iwechat.Account {
	return f.accounts
}

func TestKeysUsesInjectedCapabilityAndPersistsState(t *testing.T) {
	state, err := ctx.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	account := &iwechat.Account{Name: "fixture", PID: 7, Version: 4}
	state.SwitchCurrent(account)
	service := &keyServiceFixture{accounts: []*iwechat.Account{account}}
	media := &mediaKeysFixture{}
	keys := NewKeys(state, service, keyCaptureFixture{}, media)

	if err := keys.GetDataKey(); err != nil {
		t.Fatal(err)
	}
	if state.DataKey != "fixture-data-key" {
		t.Fatalf("data key = %q", state.DataKey)
	}
	if err := keys.GetImageKey(nil); err != nil {
		t.Fatal(err)
	}
	if state.ImgKey != "fixture-image-key" {
		t.Fatalf("image key = %q", state.ImgKey)
	}
	if media.imageKey != "fixture-image-key" {
		t.Fatalf("media image key = %q", media.imageKey)
	}
}
