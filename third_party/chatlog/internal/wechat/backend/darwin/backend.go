package darwin

import (
	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/wechat/backend"
	"github.com/sjzar/chatlog/internal/wechat/decrypt"
	decryptdarwin "github.com/sjzar/chatlog/internal/wechat/decrypt/darwin"
	"github.com/sjzar/chatlog/internal/wechat/key"
	keydarwin "github.com/sjzar/chatlog/internal/wechat/key/darwin"
	"github.com/sjzar/chatlog/internal/wechat/model"
	"github.com/sjzar/chatlog/internal/wechat/process"
	processdarwin "github.com/sjzar/chatlog/internal/wechat/process/darwin"
)

const Version4 = 4

// Backend is the sole production platform module.
type Backend struct{}

func New() *Backend {
	return &Backend{}
}

func (b *Backend) ID() string {
	return model.PlatformDarwin
}

func (b *Backend) Detector() process.Detector {
	return processdarwin.NewDetector()
}

func (b *Backend) NewExtractor(version int) (key.Extractor, error) {
	if version != Version4 {
		return nil, errors.PlatformUnsupported(b.ID(), version)
	}
	return keydarwin.NewV4Extractor(), nil
}

func (b *Backend) NewDecryptor(version int) (decrypt.Decryptor, error) {
	if version != Version4 {
		return nil, errors.PlatformUnsupported(b.ID(), version)
	}
	return decryptdarwin.NewV4Decryptor(), nil
}

var _ backend.Backend = (*Backend)(nil)
