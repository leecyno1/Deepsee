package help

import (
	"fmt"

	"github.com/sjzar/chatlog/internal/ui/style"

	"github.com/rivo/tview"
)

const (
	Title     = "help"
	ShowTitle = "帮助"
	Content   = `[yellow]Chatlog 使用指南[white]

[green]基本操作:[white]
• 使用 [yellow]←→[white] 在主菜单和帮助页面之间切换
• 使用 [yellow]↑↓[white] 移动，按 [yellow]Enter[white] 选择，按 [yellow]Esc[white] 返回
• 按 [yellow]Ctrl+C[white] 退出程序

[green]使用步骤:[white]

[yellow]1. 准备微信聊天数据[white]
   如需迁移手机记录，请在手机微信中选择「我 - 设置 - 通用 - 聊天记录迁移与备份」。

[yellow]2. 获取密钥并启动查询[white]
   macOS 数据库密钥使用 Frida 获取：先执行 [yellow]pip3 install frida-tools[white]，
   再选择「重启并获取数据库密钥」或运行 [yellow]chatlog ops database-key[white]。
   图片密钥为独立流程；权限不足时仅对短生命周期扫描子进程请求管理员授权。

[yellow]3. 启动 HTTP 服务[white]
   选择「启动 HTTP 服务」，浏览器打开 [yellow]http://127.0.0.1:5030[white]。
   控制面强制绑定本机回环地址，不允许直接暴露到局域网或公网。

[green]HTTP API 示例:[white]
• 会话列表: [yellow]GET http://127.0.0.1:5030/api/v1/sessions?limit=20[white]
• 聊天记录: [yellow]GET http://127.0.0.1:5030/api/v1/history?chat=wxid_xxx&time=2026-01-01[white]
• 消息搜索: [yellow]GET http://127.0.0.1:5030/api/v1/search?keyword=项目&chats=工作群[white]
• 全局搜索: [yellow]GET http://127.0.0.1:5030/api/v1/db/search?keyword=项目&mode=deep[white]
• 联系人: [yellow]GET http://127.0.0.1:5030/api/v1/contacts?limit=20[white]
• 群聊: [yellow]GET http://127.0.0.1:5030/api/v1/chatrooms?limit=20[white]
• 朋友圈: [yellow]GET http://127.0.0.1:5030/api/v1/sns_feed?limit=20[white]

[green]前端页面:[white]
• 仪表盘、数据库浏览和全局搜索
• 会话、群聊、联系人、朋友圈和消息检索
• 关键词/指定对象推送，支持 POST、Hermes Weixin、Hermes QQ

[green]数据安全:[white]
• 配置和密钥文件使用仅当前用户可读写的权限
• Hermes Token 和 Client Secret 不会通过状态接口回显
• 请妥善保管解密数据，不要通过反向代理公开控制台`
)

type Help struct {
	*tview.TextView
	title string
}

func New() *Help {
	help := &Help{
		TextView: tview.NewTextView(),
		title:    Title,
	}

	help.SetDynamicColors(true)
	help.SetRegions(true)
	help.SetWrap(true)
	help.SetTextAlign(tview.AlignLeft)
	help.SetBorder(true)
	help.SetBorderColor(style.BorderColor)
	help.SetTitle(ShowTitle)

	fmt.Fprint(help, Content)

	return help
}
