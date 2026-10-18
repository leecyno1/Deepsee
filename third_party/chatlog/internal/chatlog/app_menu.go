package chatlog

import (
	"strings"

	"github.com/sjzar/chatlog/internal/ui/menu"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const httpServerMenuIndex = 4

func (a *App) inputCapture(event *tcell.EventKey) *tcell.EventKey {

	// 如果当前页面不是主页面，ESC 键返回主页面
	if a.mainPages.HasPage("submenu") && event.Key() == tcell.KeyEscape {
		a.mainPages.RemovePage("submenu")
		a.mainPages.SwitchToPage("main")
		return nil
	}

	if a.tabPages.HasFocus() {
		switch event.Key() {
		case tcell.KeyLeft:
			a.switchTab(-1)
			return nil
		case tcell.KeyRight:
			a.switchTab(1)
			return nil
		}
	}

	switch event.Key() {
	case tcell.KeyCtrlC:
		a.Stop()
	}

	return event
}

func (a *App) initMenu() {
	getImageKey := &menu.Item{
		Index:       2,
		Name:        "获取图片密钥",
		Description: "优先本地推导；内存扫描回退可能需要额外权限（微信V4）",
		Selected: func(i *menu.Item) {
			modal := tview.NewModal()
			modal.SetText("获取图片密钥\n\n正在优先尝试本地推导...\n请确保微信已登录并浏览过图片")
			a.mainPages.AddPage("modal", modal, true, true)
			a.SetFocus(modal)

			go func() {
				usedAuthorization := false
				onStatus := func(msg string) {
					if strings.Contains(msg, "管理员授权窗口") || strings.Contains(msg, "临时管理员") {
						usedAuthorization = true
					}
					text := "获取图片密钥\n\n当前状态：" + normalizeKeyProgressDetail(msg)
					a.QueueUpdateDraw(func() {
						modal.SetText(text)
					})
				}
				err := a.m.GetImageKey(onStatus)

				// 在主线程中更新UI
				a.QueueUpdateDraw(func() {
					if err != nil {
						modal.SetText("获取图片密钥失败\n\n" + normalizeKeyProgressDetail(err.Error()) + "\n\n未保留管理员权限。")
					} else {
						permissionResult := "✓ 本次无需管理员授权"
						if usedAuthorization {
							permissionResult = "✓ 临时权限子进程已退出"
						}
						modal.SetText("获取图片密钥成功\n\n✓ 图片密钥已保存\n" + permissionResult + "\n✓ TUI 仍以普通用户运行")
					}

					// 添加确认按钮
					modal.AddButtons([]string{"OK"})
					modal.SetDoneFunc(func(buttonIndex int, buttonLabel string) {
						a.mainPages.RemovePage("modal")
					})
					a.SetFocus(modal)
				})
			}()
		},
	}

	restartAndGetDataKey := &menu.Item{
		Index:       3,
		Name:        "重启并获取数据库密钥",
		Description: "仅用 Frida 提取 data key，不扫描图片密钥（无需 root）",
		Selected: func(i *menu.Item) {
			progress := newKeyExtractionProgress()
			modal := tview.NewModal().SetText(progress.Update("[1/6] 检查 Frida 环境"))
			a.mainPages.AddPage("modal", modal, true, true)
			a.SetFocus(modal)

			go func() {
				// 定义状态更新回调
				onStatus := func(msg string) {
					text := progress.Update(msg)
					a.QueueUpdateDraw(func() {
						modal.SetText(text)
					})
				}

				err := a.m.RestartAndGetDataKey(onStatus)

				a.QueueUpdateDraw(func() {
					modal.SetText(progress.Finish(err))

					modal.AddButtons([]string{"OK"})
					modal.SetDoneFunc(func(buttonIndex int, buttonLabel string) {
						a.mainPages.RemovePage("modal")
					})
					a.SetFocus(modal)
				})
			}()
		},
	}

	httpServer := &menu.Item{
		Index:       httpServerMenuIndex,
		Name:        "启动 HTTP 服务",
		Description: "启动本地 HTTP 服务器",
		Selected: func(i *menu.Item) {
			modal := tview.NewModal()

			// 根据当前服务状态执行不同操作
			if !a.m.IsHTTPRunning() {
				// HTTP 服务未启动，启动服务
				modal.SetText("正在启动 HTTP 服务...")
				a.mainPages.AddPage("modal", modal, true, true)
				a.SetFocus(modal)

				// 在后台启动服务
				go func() {
					err := a.m.StartService()

					// 在主线程中更新UI
					a.QueueUpdateDraw(func() {
						if err != nil {
							// 启动失败
							modal.SetText("启动 HTTP 服务失败: " + err.Error())
						} else {
							// 启动成功
							modal.SetText("已启动 HTTP 服务")
						}

						// 更改菜单项名称
						a.updateMenuItemsState()

						// 添加确认按钮
						modal.AddButtons([]string{"OK"})
						modal.SetDoneFunc(func(buttonIndex int, buttonLabel string) {
							a.mainPages.RemovePage("modal")
						})
						a.SetFocus(modal)
					})
				}()
			} else {
				// HTTP 服务已启动，停止服务
				modal.SetText("正在停止 HTTP 服务...")
				a.mainPages.AddPage("modal", modal, true, true)
				a.SetFocus(modal)

				// 在后台停止服务
				go func() {
					err := a.m.StopService()

					// 在主线程中更新UI
					a.QueueUpdateDraw(func() {
						if err != nil {
							// 停止失败
							modal.SetText("停止 HTTP 服务失败: " + err.Error())
						} else {
							// 停止成功
							modal.SetText("已停止 HTTP 服务")
						}

						// 更改菜单项名称
						a.updateMenuItemsState()

						// 添加确认按钮
						modal.AddButtons([]string{"OK"})
						modal.SetDoneFunc(func(buttonIndex int, buttonLabel string) {
							a.mainPages.RemovePage("modal")
						})
						a.SetFocus(modal)
					})
				}()
			}
		},
	}

	setting := &menu.Item{
		Index:       5,
		Name:        "设置",
		Description: "设置应用程序选项",
		Selected:    a.settingSelected,
	}

	selectAccount := &menu.Item{
		Index:       6,
		Name:        "切换账号",
		Description: "切换当前操作的账号，可以选择进程或历史账号",
		Selected:    a.selectAccountSelected,
	}

	a.menu.AddItem(getImageKey)
	a.menu.AddItem(restartAndGetDataKey)
	a.menu.AddItem(httpServer)
	a.menu.AddItem(setting)
	a.menu.AddItem(selectAccount)

	a.menu.AddItem(&menu.Item{
		Index:       7,
		Name:        "退出",
		Description: "退出程序",
		Selected: func(i *menu.Item) {
			a.Stop()
		},
	})
}

// settingItem 表示一个设置项
