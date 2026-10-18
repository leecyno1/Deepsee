package chatlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/ctx"
	"github.com/sjzar/chatlog/internal/ui/footer"
	"github.com/sjzar/chatlog/internal/ui/help"
	"github.com/sjzar/chatlog/internal/ui/infobar"
	"github.com/sjzar/chatlog/internal/ui/menu"
	"github.com/sjzar/chatlog/internal/wechat"

	"github.com/rivo/tview"
)

const (
	RefreshInterval = 1000 * time.Millisecond
)

type App struct {
	*tview.Application

	ctx         *ctx.Context
	m           *Manager
	stopRefresh chan struct{}
	stopOnce    sync.Once
	refreshWG   sync.WaitGroup
	// UI/process state still refreshes once per second, while SessionTable reads
	// happen only after WCDB message/session filesystem events.
	sessionWake                  chan struct{}
	sessionSubscriptionCancels   []func()
	sessionSubscriptionAttempted bool

	// page
	mainPages *tview.Pages
	infoBar   *infobar.InfoBar
	tabPages  *tview.Pages
	footer    *footer.Footer

	// tab
	menu      *menu.Menu
	help      *help.Help
	activeTab int
	tabCount  int
}

func NewApp(ctx *ctx.Context, m *Manager) *App {
	app := &App{
		ctx:         ctx,
		m:           m,
		Application: tview.NewApplication(),
		stopRefresh: make(chan struct{}),
		sessionWake: make(chan struct{}, 1),
		mainPages:   tview.NewPages(),
		infoBar:     infobar.New(),
		tabPages:    tview.NewPages(),
		footer:      footer.New(),
		menu:        menu.New("主菜单"),
		help:        help.New(),
	}

	app.initMenu()

	app.updateMenuItemsState()

	return app
}

func (a *App) Run() error {

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.infoBar, infobar.InfoBarViewHeight, 0, false).
		AddItem(a.tabPages, 0, 1, true).
		AddItem(a.footer, 1, 1, false)

	a.mainPages.AddPage("main", flex, true, true)

	a.tabPages.
		AddPage("0", a.menu, true, true).
		AddPage("1", a.help, true, false)
	a.tabCount = 2

	a.SetInputCapture(a.inputCapture)

	a.refreshWG.Add(1)
	go func() {
		defer a.refreshWG.Done()
		a.refresh()
	}()
	defer func() {
		a.stopRefreshLoop()
		a.refreshWG.Wait()
	}()

	if err := a.SetRoot(a.mainPages, true).EnableMouse(false).Run(); err != nil {
		return err
	}

	return nil
}

func (a *App) Stop() {
	a.stopRefreshLoop()
	a.Application.Stop()
}

func (a *App) stopRefreshLoop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		if a.stopRefresh != nil {
			close(a.stopRefresh)
		}
	})
}

func (a *App) updateMenuItemsState() {
	// Menu must reflect the live listener, not the persisted auto-start preference.
	running := a.m != nil && a.m.IsHTTPRunning()
	for _, item := range a.menu.GetItems() {
		// 更新HTTP服务菜单项
		if item.Index == httpServerMenuIndex {
			if running {
				item.Name = "停止 HTTP 服务"
				item.Description = "停止本地 HTTP 服务器"
			} else {
				item.Name = "启动 HTTP 服务"
				item.Description = "启动本地 HTTP 服务器"
			}
		}
	}
}

func (a *App) switchTab(step int) {
	index := (a.activeTab + step) % a.tabCount
	if index < 0 {
		index = a.tabCount - 1
	}
	a.activeTab = index
	a.tabPages.SwitchToPage(fmt.Sprint(a.activeTab))
}

func (a *App) refresh() {
	tick := time.NewTicker(RefreshInterval)
	defer tick.Stop()
	defer a.clearSessionSubscriptions()

	for {
		select {
		case <-a.stopRefresh:
			return
		case <-tick.C:
			var processErr error
			// 对齐 wechat-log 的刷新思路：每轮基于最新进程列表重绑定当前实例，避免使用陈旧对象导致状态漂移。
			instances, err := a.m.wechat.GetWeChatInstancesWithError()
			processErr = err
			if err == nil {
				a.ctx.WeChatInstances = instances
				best := selectCurrentInstance(a.ctx.Current, instances)
				switch {
				case best != nil && (a.ctx.Current == nil || a.ctx.Current.PID != best.PID || a.ctx.Current.Name != best.Name):
					a.ctx.SwitchCurrent(best)
				case best != nil:
					// 同一个实例也刷新字段，确保 Status/DataDir 与 detector 最新结果一致。
					a.ctx.Current = best
					a.ctx.Refresh()
				case a.ctx.Current != nil:
					// 当前实例已不在进程列表中，标记离线并刷新展示。
					a.ctx.Current.Status = "offline"
					a.ctx.Refresh()
				}
			}

			httpRunning := a.m != nil && a.m.IsHTTPRunning()
			a.refreshLatestSessionOnChange(httpRunning)
			// Keep menu labels in sync with the live listener (not HTTPEnabled preference).
			a.updateMenuItemsState()
			a.infoBar.UpdateAccount(a.ctx.Account)
			a.infoBar.UpdateBasicInfo(a.ctx.PID, a.ctx.FullVersion, a.ctx.ExePath)
			statusText := a.ctx.Status
			if a.ctx.PID == 0 && processErr != nil {
				statusText = fmt.Sprintf("[red]获取进程失败: %v[white]", processErr)
			}
			a.infoBar.UpdateStatus(statusText)
			a.infoBar.UpdateDataKey(a.ctx.DataKey)
			a.infoBar.UpdateImageKey(a.ctx.ImgKey)
			a.infoBar.UpdatePlatform(a.ctx.Platform)
			a.infoBar.UpdateDataUsageDir(a.ctx.DataUsage, a.ctx.DataDir)
			allKeysPath, allKeysStatus := resolveAllKeysDisplay(a.ctx.DataDir)
			a.infoBar.UpdateAllKeys(allKeysStatus, allKeysPath)
			if a.ctx.LastSession.Unix() > 1000000000 {
				a.infoBar.UpdateSession(a.ctx.LastSession.Format("2006-01-02 15:04:05"))
			}
			if httpRunning {
				a.infoBar.UpdateHTTPServer(fmt.Sprintf("[green][已启动][white] [%s]", a.ctx.HTTPAddr))
			} else {
				a.infoBar.UpdateHTTPServer("[未启动]")
			}
			a.Draw()
		}
	}
}

func (a *App) refreshLatestSessionOnChange(httpRunning bool) {
	if !httpRunning {
		a.clearSessionSubscriptions()
		a.sessionSubscriptionAttempted = false
		return
	}
	if !a.sessionSubscriptionAttempted {
		a.sessionSubscriptionAttempted = true
		callback := func(event fsnotify.Event) error {
			if event.Op.Has(fsnotify.Create) || event.Op.Has(fsnotify.Write) ||
				event.Op.Has(fsnotify.Rename) || event.Op.Has(fsnotify.Remove) {
				select {
				case a.sessionWake <- struct{}{}:
				default:
				}
			}
			return nil
		}
		for _, group := range []string{"session", "message"} {
			cancel, err := a.m.SubscribeDatabaseChanges(group, callback)
			if err != nil {
				log.Debug().Err(err).Str("group", group).Msg("subscribe TUI session changes")
				a.clearSessionSubscriptions()
				return
			}
			a.sessionSubscriptionCancels = append(a.sessionSubscriptionCancels, cancel)
		}
	}
	select {
	case <-a.sessionWake:
		for {
			select {
			case <-a.sessionWake:
			default:
				goto refresh
			}
		}
	refresh:
		if session, err := a.m.RefreshLatestSession(); err == nil && session != nil {
			sender := session.NickName
			if sender == "" {
				sender = session.UserName
			}
			a.footer.UpdateLatestMessage(sender, session.NTime.Format("15:04:05"), session.Content)
		}
	default:
	}
}

func (a *App) clearSessionSubscriptions() {
	for _, cancel := range a.sessionSubscriptionCancels {
		if cancel != nil {
			cancel()
		}
	}
	a.sessionSubscriptionCancels = nil
	for {
		select {
		case <-a.sessionWake:
		default:
			return
		}
	}
}

func selectCurrentInstance(current *wechat.Account, instances []*wechat.Account) *wechat.Account {
	if len(instances) == 0 {
		return nil
	}
	if current == nil {
		return instances[0]
	}

	// 1) PID 精确匹配（最可靠）
	for _, inst := range instances {
		if inst != nil && current.PID != 0 && inst.PID == current.PID {
			return inst
		}
	}
	// 2) Name 匹配（账号切换场景）
	for _, inst := range instances {
		if inst != nil && current.Name != "" && inst.Name == current.Name {
			return inst
		}
	}
	// 3) ExePath 匹配（重启后 PID 变化）
	for _, inst := range instances {
		if inst != nil && current.ExePath != "" && inst.ExePath == current.ExePath {
			return inst
		}
	}
	// 4) 回退：使用第一项
	return instances[0]
}

func resolveAllKeysDisplay(dataDir string) (path string, status string) {
	clean := filepath.Clean(dataDir)
	if clean == "" || clean == "." {
		return "all_keys.json", "[yellow]未配置数据目录[white]"
	}
	base := filepath.Base(clean)
	if base == "db_storage" {
		path = filepath.Join(filepath.Dir(clean), "all_keys.json")
	} else {
		path = filepath.Join(clean, "all_keys.json")
	}
	if _, err := os.Stat(path); err == nil {
		return path, "[green]可读[white]"
	}
	if _, err := os.Stat(path); err != nil && os.IsPermission(err) {
		return path, "[red]无权限[white]"
	}
	return path, "[yellow]不存在[white]"
}
