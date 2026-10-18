package chatlog

import (
	"path/filepath"
	"testing"

	"github.com/sjzar/chatlog/internal/chatlog/ctx"
	"github.com/sjzar/chatlog/internal/chatlog/database"
	chathttp "github.com/sjzar/chatlog/internal/chatlog/http"
	"github.com/sjzar/chatlog/internal/chatlog/modules"
	"github.com/sjzar/chatlog/internal/wechatdb"
)

func TestRefreshStopSignalIsInitializedAndIdempotent(t *testing.T) {
	app := NewApp(nil, nil)
	if app.stopRefresh == nil {
		t.Fatal("refresh stop channel is nil")
	}
	app.stopRefreshLoop()
	app.stopRefreshLoop()

	select {
	case <-app.stopRefresh:
	default:
		t.Fatal("refresh stop channel is still open")
	}
}

func newHTTPPreferenceManager(t *testing.T) (*Manager, string) {
	t.Helper()
	configDir := filepath.Join(t.TempDir(), ".chatlog")
	appContext, err := ctx.New(configDir)
	if err != nil {
		t.Fatal(err)
	}
	appContext.Account = "wxid_http_test"
	appContext.WorkDir = filepath.Join(configDir, "workspaces", appContext.Account)
	appContext.HTTPAddr = "127.0.0.1:0"
	appContext.HTTPEnabled = true
	appContext.UpdateConfig()

	dbService := database.NewService(
		appContext,
		func(string, string) (*wechatdb.DB, error) { return nil, nil },
		nil,
	)
	httpService := chathttp.NewService(appContext, dbService, nil, nil)
	services := &modules.Services{Database: dbService, HTTP: httpService}
	return &Manager{
		ctx: appContext, db: dbService, http: httpService, services: services,
	}, configDir
}

func TestRememberedHTTPEnabledStateRestoresAndSurvivesShutdown(t *testing.T) {
	manager, configDir := newHTTPPreferenceManager(t)
	if err := manager.restoreHTTPService(); err != nil {
		t.Fatal(err)
	}
	if !manager.IsHTTPRunning() {
		t.Fatal("remembered HTTP enabled state did not restore the listener")
	}
	if err := manager.stopService(); err != nil {
		t.Fatal(err)
	}
	if manager.IsHTTPRunning() {
		t.Fatal("listener is still running after process-style shutdown")
	}
	if !manager.ctx.HTTPEnabled {
		t.Fatal("process shutdown erased the remembered enabled state")
	}

	reloaded, err := ctx.New(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.HTTPEnabled {
		t.Fatal("enabled state was not persisted for the next TUI launch")
	}
}

func TestExplicitHTTPStopPersistsDisabledState(t *testing.T) {
	manager, configDir := newHTTPPreferenceManager(t)
	if err := manager.restoreHTTPService(); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopService(); err != nil {
		t.Fatal(err)
	}
	if manager.ctx.HTTPEnabled {
		t.Fatal("explicit stop did not remember the disabled state")
	}

	reloaded, err := ctx.New(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.HTTPEnabled {
		t.Fatal("disabled state was not persisted for the next TUI launch")
	}
}
