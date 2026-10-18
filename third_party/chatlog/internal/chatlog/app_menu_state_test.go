package chatlog

import (
	"testing"

	"github.com/sjzar/chatlog/internal/ui/menu"
)

func TestUpdateMenuItemsStateOnlyChangesHTTPItem(t *testing.T) {
	httpItem := &menu.Item{Index: httpServerMenuIndex, Name: "旧 HTTP 状态"}
	settingsItem := &menu.Item{Index: 5, Name: "设置", Description: "设置应用程序选项"}
	app := &App{menu: menu.New("test")}
	app.menu.AddItem(httpItem)
	app.menu.AddItem(settingsItem)

	app.updateMenuItemsState()

	if httpItem.Name != "启动 HTTP 服务" {
		t.Fatalf("HTTP item name = %q", httpItem.Name)
	}
	if settingsItem.Name != "设置" || settingsItem.Description != "设置应用程序选项" {
		t.Fatalf("settings item was overwritten: %+v", settingsItem)
	}
}
