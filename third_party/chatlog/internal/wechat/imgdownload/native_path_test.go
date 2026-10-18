package imgdownload

import (
	"path/filepath"
	"testing"
)

func TestNativeDownloadPath(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "account")
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{name: "relative base", req: Request{DataDir: root, MediaPath: "msg/attach/a/Img/key"}, want: filepath.Join(root, "msg/attach/a/Img/key.dat")},
		{name: "absolute dat", req: Request{DataDir: root, MediaPath: filepath.Join(root, "msg/Img/key.dat")}, want: filepath.Join(root, "msg/Img/key.dat")},
		{name: "thumbnail", req: Request{DataDir: root, MediaPath: "msg/Img/key_t.dat"}, want: filepath.Join(root, "msg/Img/key.dat")},
		{name: "missing root", req: Request{MediaPath: "msg/Img/key"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nativeDownloadPath(tt.req); got != tt.want {
				t.Fatalf("nativeDownloadPath() = %q, want %q", got, tt.want)
			}
		})
	}
}
