//go:build cgo && (sqlite_fts5 || fts5)

package wcdb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjzar/chatlog/internal/wechat/decrypt"
	decryptdarwin "github.com/sjzar/chatlog/internal/wechat/decrypt/darwin"
	"github.com/sjzar/chatlog/internal/wechatdb/wcdbapi"
)

func TestSearchMessagesUsesMMFTSNativeForHanPhrase(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ftsPath := filepath.Join(messageDir, "message_fts.db")
	db, err := sql.Open("sqlite3", ftsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE name2id(username TEXT PRIMARY KEY)`,
		`INSERT INTO name2id(rowid, username) VALUES (1, 'talker-a'), (2, 'sender-a')`,
		`CREATE VIRTUAL TABLE message_fts_v4_0 USING fts5(
			tokenize = 'MMFtsTokenizer disable_pinyin',
			acontent, message_local_id UNINDEXED, sort_seq UNINDEXED,
			local_type UNINDEXED, session_id UNINDEXED, sender_id UNINDEXED,
			create_time UNINDEXED)`,
		`INSERT INTO message_fts_v4_0(
			acontent, message_local_id, sort_seq, local_type,
			session_id, sender_id, create_time)
		 VALUES ('微信聊天记录', 7, 1001, 1, 1, 2, 100),
		        ('微信与聊天分开', 8, 1002, 1, 1, 2, 101),
		        ('english needle', 9, 1003, 1, 1, 2, 102)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	client, err := wcdbapi.NewClient(
		accountDir,
		strings.Repeat("22", 32),
		func() (decrypt.Decryptor, error) {
			return decryptdarwin.NewV4Decryptor(), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	dataSource := &DataSource{
		dataDir:      client.DataDir(),
		client:       client,
		searchSchema: make(map[string][]searchTableMeta),
	}

	messages, total, path, err := dataSource.SearchMessages(
		context.Background(),
		time.Time{},
		time.Time{},
		[]string{"talker-a"},
		"微信聊天",
		0,
		20,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if path != messageSearchPathMMFTSNative || total != 1 || len(messages) != 1 {
		t.Fatalf("native result path=%q total=%d messages=%+v", path, total, messages)
	}
	if messages[0].Content != "微信聊天记录" {
		t.Fatalf("native result content = %q", messages[0].Content)
	}

	_, _, englishPath, err := dataSource.SearchMessages(
		context.Background(),
		time.Time{},
		time.Time{},
		[]string{"talker-a"},
		"needle",
		0,
		20,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if englishPath != messageSearchPathFTSShadow {
		t.Fatalf("English substring path = %q", englishPath)
	}
}
