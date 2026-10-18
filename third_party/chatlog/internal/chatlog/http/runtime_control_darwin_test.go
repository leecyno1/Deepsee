//go:build darwin

package http

import "testing"

func TestRelatedRuntimeProcessClassification(t *testing.T) {
	for _, command := range []string{
		"/tmp/go-build123/b001/exe/chatlog ops server-start",
		"go run -tags sqlite_fts5 . ops server-start",
		"python3 /repo/scripts/wechat_image_download_frida.py --daemon",
		"python3 -m glmocr.server --config /Users/test/.chatlog/ocr-runtime/config.yaml",
		"/Users/test/.chatlog/ocr-runtime/venv/bin/python -m mlx_vlm.server",
	} {
		if !relatedRuntimeProcess(command) {
			t.Errorf("related process was not classified: %q", command)
		}
	}
	for _, command := range []string{
		"/Applications/WeChat.app/Contents/MacOS/WeChat",
		"/opt/homebrew/bin/java -jar /Users/test/.local/share/gitflic-runner/chatlog-alpha/runner.jar",
		"/usr/bin/python3 /tmp/unrelated.py",
	} {
		if relatedRuntimeProcess(command) {
			t.Errorf("unrelated process was classified: %q", command)
		}
	}
}

func TestWeChatMainProcessClassification(t *testing.T) {
	if !isWeChatMainProcess("/Applications/WeChat.app/Contents/MacOS/WeChat") {
		t.Fatal("main WeChat process was not classified")
	}
	if isWeChatMainProcess("/Applications/WeChat.app/Contents/Frameworks/WeChatAppEx Framework.framework/Helpers/WeChatAppEx") {
		t.Fatal("WeChat helper was classified as the main process")
	}
}
