package http

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sjzar/chatlog/internal/chatlog/conf"
)

type lifecycleConfig struct{}

func (lifecycleConfig) GetHTTPAddr() string               { return "127.0.0.1:0" }
func (lifecycleConfig) GetDataDir() string                { return "" }
func (lifecycleConfig) GetRuntimeDir() string             { return "" }
func (lifecycleConfig) GetSaveDecryptedMedia() bool       { return false }
func (lifecycleConfig) GetDataKey() string                { return "" }
func (lifecycleConfig) GetWorkDir() string                { return "" }
func (lifecycleConfig) GetVersion() int                   { return 4 }
func (lifecycleConfig) GetMessageHook() *conf.MessageHook { return &conf.MessageHook{} }
func (lifecycleConfig) UpdateMessageHook(conf.MessageHook) error {
	return nil
}

func TestServiceCanStartStopAndStartAgain(t *testing.T) {
	s := &Service{
		conf:   lifecycleConfig{},
		router: gin.New(),
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
}

type imageKeyProviderFixture struct {
	calls int
	key   string
}

func (p *imageKeyProviderFixture) RefreshImageKey(string) (string, error) {
	p.calls++
	return p.key, nil
}

type mediaCodecFixture struct {
	key string
}

func (m *mediaCodecFixture) SetImageKey(key string) {
	m.key = key
}
func (*mediaCodecFixture) RefreshXOR(string) (byte, error) {
	return 0, nil
}
func (*mediaCodecFixture) Decode([]byte) ([]byte, string, error) {
	return nil, "", nil
}

func TestImageKeyRefreshUsesInjectedProvider(t *testing.T) {
	provider := &imageKeyProviderFixture{key: "fixture-image-key"}
	media := &mediaCodecFixture{}
	s := &Service{
		conf:      lifecycleConfig{},
		imageKeys: provider,
		media:     media,
	}
	key, err := s.tryRefreshImageKeyFromWeChat()
	if err != nil {
		t.Fatal(err)
	}
	if key != provider.key || provider.calls != 1 {
		t.Fatalf("refresh result = %q, calls = %d", key, provider.calls)
	}
	if media.key != provider.key {
		t.Fatalf("media key = %q", media.key)
	}
}
