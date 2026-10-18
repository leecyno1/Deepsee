// Package modules is the application composition root. It is the only place
// that binds domain interfaces to production platform and storage adapters.
package modules

import (
	stderrors "errors"
	"fmt"

	adapterdarwin "github.com/sjzar/chatlog/internal/chatlog/adapters/darwin"
	adaptermedia "github.com/sjzar/chatlog/internal/chatlog/adapters/media"
	"github.com/sjzar/chatlog/internal/chatlog/database"
	chathttp "github.com/sjzar/chatlog/internal/chatlog/http"
	"github.com/sjzar/chatlog/internal/chatlog/messagehook"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	chatwechat "github.com/sjzar/chatlog/internal/chatlog/wechat"
	"github.com/sjzar/chatlog/internal/wechat"
	"github.com/sjzar/chatlog/internal/wechat/backend"
	backenddarwin "github.com/sjzar/chatlog/internal/wechat/backend/darwin"
	"github.com/sjzar/chatlog/internal/wechat/decrypt"
	"github.com/sjzar/chatlog/internal/wechatdb"
	"github.com/sjzar/chatlog/internal/wechatdb/datasource/wcdb"
)

type Config interface {
	database.Config
	chathttp.Config
}

// Adapters lists every infrastructure capability required by the application.
// Keeping the concrete bindings in one value makes composition explicit and
// prevents feature packages from reaching into platform implementations.
type Adapters struct {
	Platform   backend.Backend
	OpenDB     database.OpenDB
	OpenHook   database.HookFactory
	KeyCapture ports.KeyCapture
	MediaKeys  ports.MediaCodec
}

// Factory contains replaceable application adapters. New platform or storage
// modules can be introduced without changing domain or transport services.
type Factory struct {
	adapters Adapters
}

func NewFactory(adapters Adapters) *Factory {
	return &Factory{adapters: adapters}
}

func DarwinFactory() *Factory {
	platform := backenddarwin.New()
	openDB := func(path, dataKey string) (*wechatdb.DB, error) {
		ds, err := wcdb.New(path, dataKey, func() (decrypt.Decryptor, error) {
			return platform.NewDecryptor(backenddarwin.Version4)
		})
		if err != nil {
			return nil, err
		}
		return wechatdb.New(ds)
	}
	return NewFactory(Adapters{
		Platform: platform,
		OpenDB:   openDB,
		OpenHook: func(conf database.Config, db database.HookDatabase) (database.HookRuntime, error) {
			return messagehook.New(conf, db)
		},
		KeyCapture: adapterdarwin.NewKeyCapture(),
		MediaKeys:  adaptermedia.NewKeys(),
	})
}

type Services struct {
	Accounts   *wechat.Manager
	WeChat     *chatwechat.Service
	Database   *database.Service
	HTTP       *chathttp.Service
	KeyCapture ports.KeyCapture
	MediaKeys  ports.MediaCodec
}

func (f *Factory) Build(conf Config) (*Services, error) {
	switch {
	case f == nil:
		return nil, fmt.Errorf("module factory is not configured")
	case f.adapters.Platform == nil:
		return nil, fmt.Errorf("platform backend is not configured")
	case f.adapters.OpenDB == nil:
		return nil, fmt.Errorf("database opener is not configured")
	case f.adapters.OpenHook == nil:
		return nil, fmt.Errorf("message hook factory is not configured")
	case f.adapters.KeyCapture == nil:
		return nil, fmt.Errorf("key capture adapter is not configured")
	case f.adapters.MediaKeys == nil:
		return nil, fmt.Errorf("media key adapter is not configured")
	case conf == nil:
		return nil, fmt.Errorf("module config is not configured")
	}
	accounts := wechat.NewManager(f.adapters.Platform)
	wechatService := chatwechat.NewService(accounts)
	databaseService := database.NewService(conf, f.adapters.OpenDB, f.adapters.OpenHook)
	httpService := chathttp.NewService(conf, databaseService, wechatService, f.adapters.MediaKeys)
	return &Services{
		Accounts:   accounts,
		WeChat:     wechatService,
		Database:   databaseService,
		HTTP:       httpService,
		KeyCapture: f.adapters.KeyCapture,
		MediaKeys:  f.adapters.MediaKeys,
	}, nil
}

func (s *Services) StartHTTP() error {
	if err := s.Database.Start(); err != nil {
		return err
	}
	if err := s.HTTP.Start(); err != nil {
		_ = s.Database.Stop()
		return err
	}
	return nil
}

func (s *Services) Stop() error {
	var errs []error
	if s.HTTP != nil {
		errs = append(errs, s.HTTP.Stop())
	}
	if s.Database != nil {
		errs = append(errs, s.Database.Stop())
	}
	return stderrors.Join(errs...)
}
