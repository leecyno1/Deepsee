package chatlog

import (
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ctx"
	"github.com/sjzar/chatlog/internal/chatlog/database"
	"github.com/sjzar/chatlog/internal/chatlog/http"
	"github.com/sjzar/chatlog/internal/chatlog/modules"
	"github.com/sjzar/chatlog/internal/chatlog/usecase"
	"github.com/sjzar/chatlog/internal/chatlog/wechat"
	iwechat "github.com/sjzar/chatlog/internal/wechat"
	"github.com/sjzar/chatlog/pkg/config"
)

// Manager 管理聊天日志应用
type Manager struct {
	ctx *ctx.Context
	sc  *conf.ServerConfig
	scm *config.Manager

	// Services
	db       *database.Service
	http     *http.Service
	wechat   *wechat.Service
	services *modules.Services
	factory  *modules.Factory
	keys     *usecase.Keys
	sessions *usecase.Sessions

	// Terminal UI
	app *App
}

func New() *Manager {
	return NewWithFactory(modules.DarwinFactory())
}

func NewWithFactory(factory *modules.Factory) *Manager {
	return &Manager{factory: factory}
}

func (m *Manager) compose(conf modules.Config) error {
	if m.factory == nil {
		m.factory = modules.DarwinFactory()
	}
	services, err := m.factory.Build(conf)
	if err != nil {
		return err
	}
	m.services = services
	m.wechat = m.services.WeChat
	m.db = m.services.Database
	m.http = m.services.HTTP
	if m.ctx != nil {
		m.keys = usecase.NewKeys(m.ctx, m.wechat, m.services.KeyCapture, m.services.MediaKeys)
		m.sessions = usecase.NewSessions(m.ctx, m.db)
	}
	return nil
}

func (m *Manager) Run(configPath string) (runErr error) {

	var err error
	m.ctx, err = ctx.New(configPath)
	if err != nil {
		return err
	}

	if err := m.compose(m.ctx); err != nil {
		return err
	}
	defer func() {
		if err := m.stopService(); err != nil && runErr == nil {
			runErr = err
		}
	}()

	m.ctx.WeChatInstances = m.wechat.GetWeChatInstances()
	if len(m.ctx.WeChatInstances) >= 1 {
		m.ctx.SwitchCurrent(m.ctx.WeChatInstances[0])
	}

	// HTTPEnabled is the user's persisted TUI preference. Restore the listener
	// after the account context has been selected; a launch failure is surfaced
	// in logs while leaving the preference intact for the next retry.
	if m.ctx.HTTPEnabled {
		if err := m.restoreHTTPService(); err != nil {
			log.Error().Err(err).Msg("restore remembered HTTP service state failed")
		}
	}
	// 启动终端UI
	m.app = NewApp(m.ctx, m)
	return m.app.Run() // 阻塞；退出后由 defer 统一停止 HTTP 和数据库
}

func (m *Manager) Switch(info *iwechat.Account, history string) error {
	// Stop infrastructure without changing the source account's saved
	// preference, then load and apply the destination account's preference.
	if m.IsHTTPRunning() {
		if err := m.stopService(); err != nil {
			return err
		}
	}
	if info != nil {
		m.ctx.SwitchCurrent(info)
	} else {
		m.ctx.SwitchHistory(history)
	}
	if m.ctx.HTTPEnabled {
		if err := m.restoreHTTPService(); err != nil {
			return fmt.Errorf("restore HTTP service for %s: %w", m.ctx.Account, err)
		}
	}
	return nil
}
