package chatlog

import (
	"path/filepath"
	"testing"

	clog "github.com/sjzar/chatlog/pkg/log"
)

func TestClosingLogFileStopsRotatingWriter(t *testing.T) {
	writer := clog.Init(filepath.Join(t.TempDir(), "chatlog.log"), clog.DefaultRetentionDays)
	if _, err := writer.Write([]byte("open\n")); err != nil {
		t.Fatal(err)
	}
	closeLogFile()
	if _, err := writer.Write([]byte("closed\n")); err == nil {
		t.Fatal("closed rotating writer is still writable")
	}
}
