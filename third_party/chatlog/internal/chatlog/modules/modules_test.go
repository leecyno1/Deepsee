package modules

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/database"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/wechat/decrypt"
	"github.com/sjzar/chatlog/internal/wechat/key"
	"github.com/sjzar/chatlog/internal/wechat/model"
	"github.com/sjzar/chatlog/internal/wechat/process"
	"github.com/sjzar/chatlog/internal/wechatdb"
)

type configFixture struct{}

func (configFixture) GetAccount() string                       { return "" }
func (configFixture) GetDataDir() string                       { return "" }
func (configFixture) GetDataKey() string                       { return "" }
func (configFixture) GetHTTPAddr() string                      { return "127.0.0.1:0" }
func (configFixture) GetMessageHook() *conf.MessageHook        { return &conf.MessageHook{} }
func (configFixture) GetRuntimeDir() string                    { return "" }
func (configFixture) GetSaveDecryptedMedia() bool              { return false }
func (configFixture) GetVersion() int                          { return 4 }
func (configFixture) GetWorkDir() string                       { return "" }
func (configFixture) UpdateMessageHook(conf.MessageHook) error { return nil }

type detectorFixture struct{}

func (detectorFixture) FindProcesses() ([]*model.Process, error) {
	return []*model.Process{{PID: 7, AccountName: "composed", Version: 4}}, nil
}

type backendFixture struct{}

func (backendFixture) ID() string                 { return "fixture" }
func (backendFixture) Detector() process.Detector { return detectorFixture{} }
func (backendFixture) NewExtractor(int) (key.Extractor, error) {
	return nil, stderrors.New("not used")
}
func (backendFixture) NewDecryptor(int) (decrypt.Decryptor, error) {
	return nil, stderrors.New("not used")
}

type keyCaptureFixture struct{}

func (keyCaptureFixture) Available() bool                   { return true }
func (keyCaptureFixture) IsImagePermissionError(error) bool { return false }
func (keyCaptureFixture) ExtractImageKey(context.Context, uint32, string, func(string)) (string, error) {
	return "", nil
}
func (keyCaptureFixture) CaptureDataKeys(context.Context, string, func(string)) (ports.DataKeyCapture, error) {
	return ports.DataKeyCapture{}, nil
}
func (keyCaptureFixture) ApplyDataKeys(string, ports.DataKeyCapture, func(string)) error {
	return nil
}

type mediaKeysFixture struct{}

func (mediaKeysFixture) SetImageKey(string) {}
func (mediaKeysFixture) RefreshXOR(string) (byte, error) {
	return 0, nil
}
func (mediaKeysFixture) Decode([]byte) ([]byte, string, error) {
	return nil, "", nil
}

type hookRuntimeFixture struct{}

func (hookRuntimeFixture) Run(context.Context) {}
func (hookRuntimeFixture) Close() error        { return nil }
func (hookRuntimeFixture) Wake()               {}
func (hookRuntimeFixture) RecentEvents(int) ([]ports.HookEvent, error) {
	return nil, nil
}
func (hookRuntimeFixture) ClearEvents() (int, error) {
	return 0, nil
}
func (hookRuntimeFixture) CancelEvent(string, string) (int64, error) { return 0, nil }
func (hookRuntimeFixture) RetryEvent(string, string) (int64, error)  { return 0, nil }
func (hookRuntimeFixture) Stats() (ports.HookRuntimeStats, error) {
	return ports.HookRuntimeStats{}, nil
}
func (hookRuntimeFixture) StorePath() string { return "" }

func TestFactoryBuildsOneConnectedServiceGraph(t *testing.T) {
	openCalls := 0
	factory := NewFactory(Adapters{
		Platform: backendFixture{},
		OpenDB: func(string, string) (*wechatdb.DB, error) {
			openCalls++
			return nil, stderrors.New("not used")
		},
		OpenHook: func(database.Config, database.HookDatabase) (database.HookRuntime, error) {
			return hookRuntimeFixture{}, nil
		},
		KeyCapture: keyCaptureFixture{},
		MediaKeys:  mediaKeysFixture{},
	})
	services, err := factory.Build(configFixture{})
	if err != nil {
		t.Fatal(err)
	}
	if services.Accounts == nil || services.WeChat == nil || services.Database == nil || services.HTTP == nil {
		t.Fatal("composition root returned an incomplete service graph")
	}
	if err := services.Accounts.Load(); err != nil {
		t.Fatal(err)
	}
	if got := services.Accounts.GetAccounts(); len(got) != 1 || got[0].Name != "composed" {
		t.Fatalf("composed accounts = %#v", got)
	}
	if openCalls != 0 {
		t.Fatal("building modules must not start infrastructure")
	}
}

func TestFactoryRejectsMissingAdapters(t *testing.T) {
	if _, err := NewFactory(Adapters{}).Build(configFixture{}); err == nil {
		t.Fatal("missing adapters were accepted")
	}
}
