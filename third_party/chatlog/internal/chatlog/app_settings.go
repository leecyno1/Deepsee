package chatlog

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sjzar/chatlog/internal/ui/form"
	"github.com/sjzar/chatlog/internal/ui/menu"
)

type settingItem struct {
	name        string
	description string
	action      func()
}

func (a *App) settingSelected(i *menu.Item) {

	settings := []settingItem{
		{
			name:        "设置 HTTP 服务地址",
			description: "配置 HTTP 服务监听的地址",
			action:      a.settingHTTPPort,
		},
		{
			name:        "设置数据密钥",
			description: "配置数据解密密钥",
			action:      a.settingDataKey,
		},
		{
			name:        "设置图片密钥",
			description: "配置图片解密密钥",
			action:      a.settingImgKey,
		},
		{
			name:        "设置数据目录",
			description: "配置微信数据文件所在目录",
			action:      a.settingDataDir,
		},
		{
			name:        "设置关键词触发器",
			description: "多个关键词使用 ｜ 分割",
			action:      a.settingHookKeywords,
		},
		{
			name:        "设置触发通知方式",
			description: "可选 post / weixin / qq / all，多个值用逗号分隔",
			action:      a.settingHookNotifyMode,
		},
		{
			name:        "设置触发 POST 地址",
			description: "通知方式包含 post 时生效",
			action:      a.settingHookPostURL,
		},
		{
			name:        "设置触发前文条数",
			description: "命中关键词前保留条数（默认5）",
			action:      a.settingHookBeforeCount,
		},
		{
			name:        "设置触发后文条数",
			description: "命中关键词后保留条数（默认5）",
			action:      a.settingHookAfterCount,
		},
	}

	subMenu := menu.NewSubMenu("设置")
	for idx, setting := range settings {
		item := &menu.Item{
			Index:       idx + 1,
			Name:        setting.name,
			Description: setting.description,
			Selected: func(action func()) func(*menu.Item) {
				return func(*menu.Item) {
					action()
				}
			}(setting.action),
		}
		subMenu.AddItem(item)
	}

	a.mainPages.AddPage("submenu", subMenu, true, true)
	a.SetFocus(subMenu)
}

// settingHTTPPort 设置 HTTP 端口
func (a *App) settingHTTPPort() {
	// 使用我们的自定义表单组件
	formView := form.NewForm("设置 HTTP 地址")

	// 临时存储用户输入的值
	tempHTTPAddr := a.ctx.HTTPAddr

	// 添加输入字段 - 不再直接设置HTTP地址，而是更新临时变量
	formView.AddInputField("地址", tempHTTPAddr, 0, nil, func(text string) {
		tempHTTPAddr = text // 只更新临时变量
	})

	// 添加按钮 - 点击保存时才设置HTTP地址
	formView.AddButton("保存", func() {
		if err := a.m.SetHTTPAddr(tempHTTPAddr); err != nil {
			a.showInfo("HTTP 地址无效: " + err.Error())
			return
		}
		a.mainPages.RemovePage("submenu2")
		a.showInfo("HTTP 地址已设置为 " + a.ctx.HTTPAddr)
	})

	formView.AddButton("取消", func() {
		a.mainPages.RemovePage("submenu2")
	})

	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

// settingWorkDir 设置工作目录
func (a *App) settingWorkDir() {
	// 使用我们的自定义表单组件
	formView := form.NewForm("设置工作目录")

	// 临时存储用户输入的值
	tempWorkDir := a.ctx.WorkDir

	// 添加输入字段 - 不再直接设置工作目录，而是更新临时变量
	formView.AddInputField("工作目录", tempWorkDir, 0, nil, func(text string) {
		tempWorkDir = text // 只更新临时变量
	})

	// 添加按钮 - 点击保存时才设置工作目录
	formView.AddButton("保存", func() {
		a.ctx.SetWorkDir(tempWorkDir) // 在这里设置工作目录
		a.mainPages.RemovePage("submenu2")
		a.showInfo("工作目录已设置为 " + a.ctx.WorkDir)
	})

	formView.AddButton("取消", func() {
		a.mainPages.RemovePage("submenu2")
	})

	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

// settingDataKey 设置数据密钥
func (a *App) settingDataKey() {
	// 使用我们的自定义表单组件
	formView := form.NewForm("设置数据密钥")

	// 临时存储用户输入的值
	tempDataKey := a.ctx.DataKey

	// 添加输入字段 - 不直接设置数据密钥，而是更新临时变量
	formView.AddInputField("数据密钥", tempDataKey, 0, nil, func(text string) {
		tempDataKey = text // 只更新临时变量
	})

	// 添加按钮 - 点击保存时才设置数据密钥
	formView.AddButton("保存", func() {
		a.ctx.DataKey = tempDataKey // 设置数据密钥
		a.mainPages.RemovePage("submenu2")
		a.showInfo("数据密钥已设置")
	})

	formView.AddButton("取消", func() {
		a.mainPages.RemovePage("submenu2")
	})

	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

// settingImgKey 设置图片密钥 (ImgKey)
func (a *App) settingImgKey() {
	formView := form.NewForm("设置图片密钥")

	tempImgKey := a.ctx.ImgKey

	formView.AddInputField("图片密钥", tempImgKey, 0, nil, func(text string) {
		tempImgKey = text
	})

	formView.AddButton("保存", func() {
		a.ctx.SetImgKey(tempImgKey)
		a.mainPages.RemovePage("submenu2")
		a.showInfo("图片密钥已设置")
	})

	formView.AddButton("取消", func() {
		a.mainPages.RemovePage("submenu2")
	})

	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

// settingDataDir 设置数据目录
func (a *App) settingDataDir() {
	// 使用我们的自定义表单组件
	formView := form.NewForm("设置数据目录")

	// 临时存储用户输入的值
	tempDataDir := a.ctx.DataDir

	// 添加输入字段 - 不直接设置数据目录，而是更新临时变量
	formView.AddInputField("数据目录", tempDataDir, 0, nil, func(text string) {
		tempDataDir = text // 只更新临时变量
	})

	// 添加按钮 - 点击保存时才设置数据目录
	formView.AddButton("保存", func() {
		a.ctx.DataDir = tempDataDir // 设置数据目录
		a.mainPages.RemovePage("submenu2")
		a.showInfo("数据目录已设置为 " + a.ctx.DataDir)
	})

	formView.AddButton("取消", func() {
		a.mainPages.RemovePage("submenu2")
	})

	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

func (a *App) settingHookKeywords() {
	formView := form.NewForm("设置关键词触发器")
	tempKeywords := a.ctx.HookKeywords
	formView.AddInputField("关键词", tempKeywords, 0, nil, func(text string) {
		tempKeywords = text
	})
	formView.AddButton("保存", func() {
		a.ctx.SetHookKeywords(tempKeywords)
		a.mainPages.RemovePage("submenu2")
		a.showInfo("关键词触发器已更新")
	})
	formView.AddButton("取消", func() {
		a.mainPages.RemovePage("submenu2")
	})
	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

func (a *App) settingHookNotifyMode() {
	formView := form.NewForm("设置触发通知方式")
	tempMode := a.ctx.HookNotifyMode
	if strings.TrimSpace(tempMode) == "" {
		tempMode = "post"
	}
	formView.AddInputField("方式", tempMode, 0, nil, func(text string) {
		tempMode = text
	})
	formView.AddButton("保存", func() {
		a.ctx.SetHookNotifyMode(tempMode)
		a.mainPages.RemovePage("submenu2")
		a.showInfo("通知方式已更新为 " + a.ctx.HookNotifyMode)
	})
	formView.AddButton("取消", func() {
		a.mainPages.RemovePage("submenu2")
	})
	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

func (a *App) settingHookPostURL() {
	formView := form.NewForm("设置触发 POST 地址")
	tempURL := a.ctx.HookPostURL
	formView.AddInputField("POST URL", tempURL, 0, nil, func(text string) {
		tempURL = text
	})
	formView.AddButton("保存", func() {
		a.ctx.SetHookPostURL(tempURL)
		a.mainPages.RemovePage("submenu2")
		a.showInfo("POST 地址已更新")
	})
	formView.AddButton("取消", func() {
		a.mainPages.RemovePage("submenu2")
	})
	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

func (a *App) settingHookBeforeCount() {
	formView := form.NewForm("设置触发前文条数")
	temp := fmt.Sprintf("%d", a.ctx.HookBeforeCount)
	formView.AddInputField("前文条数", temp, 0, nil, func(text string) { temp = text })
	formView.AddButton("保存", func() {
		n := 0
		if v, err := strconv.Atoi(strings.TrimSpace(temp)); err == nil && v >= 0 {
			n = v
		}
		a.ctx.SetHookBeforeCount(n)
		a.mainPages.RemovePage("submenu2")
		a.showInfo(fmt.Sprintf("前文条数已设置为 %d", n))
	})
	formView.AddButton("取消", func() { a.mainPages.RemovePage("submenu2") })
	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

func (a *App) settingHookAfterCount() {
	formView := form.NewForm("设置触发后文条数")
	temp := fmt.Sprintf("%d", a.ctx.HookAfterCount)
	formView.AddInputField("后文条数", temp, 0, nil, func(text string) { temp = text })
	formView.AddButton("保存", func() {
		n := 0
		if v, err := strconv.Atoi(strings.TrimSpace(temp)); err == nil && v >= 0 {
			n = v
		}
		a.ctx.SetHookAfterCount(n)
		a.mainPages.RemovePage("submenu2")
		a.showInfo(fmt.Sprintf("后文条数已设置为 %d", n))
	})
	formView.AddButton("取消", func() { a.mainPages.RemovePage("submenu2") })
	a.mainPages.AddPage("submenu2", formView, true, true)
	a.SetFocus(formView)
}

// selectAccountSelected 处理切换账号菜单项的选择事件
