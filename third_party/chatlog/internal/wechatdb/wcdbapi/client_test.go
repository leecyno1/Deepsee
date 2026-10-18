package wcdbapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechat/decrypt"
	decryptdarwin "github.com/sjzar/chatlog/internal/wechat/decrypt/darwin"
)

func newTestDecryptor() (decrypt.Decryptor, error) {
	return decryptdarwin.NewV4Decryptor(), nil
}

func TestListMessageDBsIncludesNumericBusinessShardsOnly(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"message_0.db",
		"message_123.db",
		"biz_message_0.db",
		"biz_message_12.db",
		"message_fts.db",
		"message_resource.db",
		"weclaw.db",
	} {
		createPlainSQLiteDB(t, filepath.Join(messageDir, name), `CREATE TABLE marker (id INTEGER)`)
	}

	client, err := NewClient(accountDir, strings.Repeat("11", 32), newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ListMessageDBs()
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(got))
	for _, path := range got {
		gotNames = append(gotNames, filepath.Base(path))
	}
	want := []string{"biz_message_0.db", "biz_message_12.db", "message_0.db", "message_123.db"}
	if strings.Join(gotNames, ",") != strings.Join(want, ",") {
		t.Fatalf("ListMessageDBs = %v, want %v", gotNames, want)
	}
}

func TestNewClientRejectsNonHexDataKey(t *testing.T) {
	_, err := NewClient(t.TempDir(), strings.Repeat("z", 64), newTestDecryptor)
	if err == nil || !strings.Contains(err.Error(), "encoding") {
		t.Fatalf("NewClient error = %v, want invalid encoding", err)
	}
}

func TestGetMessagesInRangeReturnsShardSchemaErrors(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const talker = "gh_public_account"
	sum := md5.Sum([]byte(talker))
	table := "Msg_" + hex.EncodeToString(sum[:])
	createMessageShard(t, filepath.Join(messageDir, "message_0.db"), table, true)
	createMessageShard(t, filepath.Join(messageDir, "message_1.db"), table, false)

	client, err := NewClient(accountDir, strings.Repeat("12", 32), newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.GetMessagesInRange(talker, 0, 0, 20, 0)
	if err == nil {
		t.Fatalf("schema-broken shard returned success with rows=%v", rows)
	}
	if !strings.Contains(err.Error(), "message_1.db") || !strings.Contains(err.Error(), "create_time") {
		t.Fatalf("shard error lacks context: %v", err)
	}
}

func TestGetMessagesAfterPagesOldestFirstWithoutBurstGaps(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const talker = "burst@chatroom"
	sum := md5.Sum([]byte(talker))
	table := "Msg_" + hex.EncodeToString(sum[:])
	createBurstMessageShard(t, filepath.Join(messageDir, "message_0.db"), table, 1, 225)
	createBurstMessageShard(t, filepath.Join(messageDir, "message_1.db"), table, 226, 450)

	client, err := NewClient(accountDir, strings.Repeat("13", 32), newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	cursor := model.MessageCursor{Timestamp: 1699999999}
	var got []int64
	for {
		rows, err := client.GetMessagesAfter(talker, cursor, 73)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			ts := toInt64(row["create_time"])
			localID := toInt64(row["local_id"])
			if ts < cursor.Timestamp || (ts == cursor.Timestamp && localID <= cursor.LocalID) {
				t.Fatalf("row did not advance cursor: cursor=%#v row=%d/%d", cursor, ts, localID)
			}
			got = append(got, localID)
			cursor = model.MessageCursor{Timestamp: ts, LocalID: localID}
		}
		if len(rows) < 73 {
			break
		}
	}
	if len(got) != 450 {
		t.Fatalf("message count = %d, want 450", len(got))
	}
	for index, id := range got {
		if want := int64(index + 1); id != want {
			t.Fatalf("message[%d] local_id = %d, want %d", index, id, want)
		}
	}
}

func TestChangedMessageShardResolvesTalkerAndLimitsIncrementalRead(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const firstTalker = "first@chatroom"
	const secondTalker = "wxid_second"
	firstHash := md5.Sum([]byte(firstTalker))
	secondHash := md5.Sum([]byte(secondTalker))
	firstPath := filepath.Join(messageDir, "message_0.db")
	secondPath := filepath.Join(messageDir, "message_1.db")
	createMessageShard(t, firstPath, "Msg_"+hex.EncodeToString(firstHash[:]), true)
	createMessageShard(t, secondPath, "Msg_"+hex.EncodeToString(secondHash[:]), true)

	client, err := NewClient(accountDir, strings.Repeat("14", 32), newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	changed := []string{firstPath + "-wal", firstPath + "-shm"}
	resolved, err := client.ResolveChangedMessageTalkers(changed, map[string]model.MessageCursor{
		secondTalker: {Timestamp: 1699999999},
		firstTalker:  {Timestamp: 1699999999},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0] != firstTalker {
		t.Fatalf("resolved talkers = %v, want [%s]", resolved, firstTalker)
	}
	rows, err := client.GetMessagesAfterFiles(firstTalker, model.MessageCursor{Timestamp: 1699999999}, 20, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || toInt64(rows[0]["local_id"]) != 1 {
		t.Fatalf("targeted rows = %#v", rows)
	}
	otherRows, err := client.GetMessagesAfterFiles(secondTalker, model.MessageCursor{Timestamp: 1699999999}, 20, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherRows) != 0 {
		t.Fatalf("unchanged shard returned second talker rows: %#v", otherRows)
	}
}

func createBurstMessageShard(t *testing.T, path, table string, first, last int64) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE Name2Id (user_name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO Name2Id(rowid, user_name) VALUES (1, 'sender')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE [` + table + `] (
		local_id INTEGER, sort_seq INTEGER, server_id INTEGER,
		local_type INTEGER, real_sender_id INTEGER, create_time INTEGER,
		message_content BLOB, compress_content BLOB, source BLOB,
		packed_info_data BLOB, status INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO [` + table + `] VALUES (?, ?, ?, 1, 1, ?, 'ok', NULL, NULL, NULL, 4)`)
	if err != nil {
		t.Fatal(err)
	}
	for id := first; id <= last; id++ {
		// Deliberately place many rows in the same second to verify the
		// timestamp/local_id tie breaker.
		if _, err := stmt.Exec(id, id, id, int64(1700000000)+id/100); err != nil {
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func createMessageShard(t *testing.T, path, table string, valid bool) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE Name2Id (user_name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if !valid {
		if _, err := db.Exec(`CREATE TABLE [` + table + `] (local_id INTEGER)`); err != nil {
			t.Fatal(err)
		}
		return
	}
	schema := `CREATE TABLE [` + table + `] (
		local_id INTEGER, sort_seq INTEGER, server_id INTEGER,
		local_type INTEGER, real_sender_id INTEGER, create_time INTEGER,
		message_content BLOB, compress_content BLOB, source BLOB,
		packed_info_data BLOB, status INTEGER
	)`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO Name2Id(rowid, user_name) VALUES (1, 'sender')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO [` + table + `] VALUES (1, 1, 1, 1, 1, 1700000000, 'ok', NULL, NULL, NULL, 4)`); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDecryptedReadsEncryptedDatabaseDirectly(t *testing.T) {
	root := t.TempDir()
	cacheRoot := filepath.Join(root, "chatlog")
	t.Setenv("CHATLOG_DIR", cacheRoot)
	accountDir := filepath.Join(root, "account")
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataKeyHex := strings.Repeat("4a", 32)
	dataKey, err := hex.DecodeString(dataKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	plain1 := createUserVersionPage(t, filepath.Join(root, "plain-direct-1.db"), 21)
	plain2 := createUserVersionPage(t, filepath.Join(root, "plain-direct-2.db"), 22)
	plain3 := createUserVersionPage(t, filepath.Join(root, "plain-direct-3.db"), 23)
	salt := []byte("direct-read-salt")
	sourcePath := filepath.Join(messageDir, "message_0.db")
	writeEncryptedMainPage(t, sourcePath, plain1, dataKey, salt)

	client, err := NewClient(accountDir, dataKeyHex, newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	directPath, err := client.ensureDirectRead(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !isDirectSQLitePath(directPath) {
		t.Fatalf("query path = %q, want direct SQLite token", directPath)
	}
	assertSQLiteUserVersion(t, directPath, 21)
	if _, err := os.Stat(filepath.Join(cacheRoot, "cache", "wcdb")); !os.IsNotExist(err) {
		t.Fatalf("direct query created decoded cache directory: %v", err)
	}

	decryptor := decryptdarwin.NewV4Decryptor()
	pageKey, _, err := decryptor.DeriveKeys(dataKey, salt)
	if err != nil {
		t.Fatal(err)
	}
	wal := make([]byte, walHeaderSize)
	binary.BigEndian.PutUint32(wal[8:12], pageSize)
	binary.BigEndian.PutUint32(wal[16:20], 0x10203040)
	binary.BigEndian.PutUint32(wal[20:24], 0x50607080)
	wal = appendWALFrame(t, wal, pageKey, 1, 1, plain2)
	wal = appendWALFrame(t, wal, pageKey, 1, 0, plain1) // uncommitted tail
	if err := os.WriteFile(sourcePath+"-wal", wal, 0o600); err != nil {
		t.Fatal(err)
	}

	walPath, err := client.ensureDirectRead(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if walPath == directPath {
		t.Fatal("WAL change reused a stale direct snapshot")
	}
	assertSQLiteUserVersion(t, walPath, 22)

	// The next commit resumes from the previous parsed offset. The pending
	// frame above becomes part of this transaction, then the newer page wins.
	wal = appendWALFrame(t, wal, pageKey, 1, 1, plain3)
	if err := os.WriteFile(sourcePath+"-wal", wal, 0o600); err != nil {
		t.Fatal(err)
	}
	appendedPath, err := client.ensureDirectRead(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLiteUserVersion(t, appendedPath, 23)
	client.directMu.Lock()
	parsedSize := client.direct[sourcePath].snapshot.walState.parsedSize
	client.directMu.Unlock()
	if parsedSize != int64(len(wal)) {
		t.Fatalf("incremental WAL parsed size = %d, want %d", parsedSize, len(wal))
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, "cache", "wcdb")); !os.IsNotExist(err) {
		t.Fatalf("WAL direct query created decoded cache directory: %v", err)
	}
}

func TestDirectSnapshotRejectsInvalidCommittedWALPageOne(t *testing.T) {
	root := t.TempDir()
	accountDir := filepath.Join(root, "account")
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataKeyHex := strings.Repeat("6c", 32)
	dataKey, err := hex.DecodeString(dataKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	plain := createUserVersionPage(t, filepath.Join(root, "plain.db"), 31)
	salt := []byte("invalid-wal-salt")
	sourcePath := filepath.Join(messageDir, "message_0.db")
	writeEncryptedMainPage(t, sourcePath, plain, dataKey, salt)

	decryptor := decryptdarwin.NewV4Decryptor()
	pageKey, _, err := decryptor.DeriveKeys(dataKey, salt)
	if err != nil {
		t.Fatal(err)
	}
	wal := make([]byte, walHeaderSize)
	binary.BigEndian.PutUint32(wal[8:12], pageSize)
	binary.BigEndian.PutUint32(wal[16:20], 0x10203040)
	binary.BigEndian.PutUint32(wal[20:24], 0x50607080)
	wal = appendWALFrame(t, wal, pageKey, 1, 1, make([]byte, pageSize))
	if err := os.WriteFile(sourcePath+"-wal", wal, 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(accountDir, dataKeyHex, newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	_, err = client.ensureDirectRead(sourcePath)
	if !errors.Is(err, errDirectSnapshotInvalid) {
		t.Fatalf("ensure direct snapshot error = %v, want %v", err, errDirectSnapshotInvalid)
	}
}

func TestLiveQueryInvalidatesAndRebuildsBrokenDirectSnapshot(t *testing.T) {
	root := t.TempDir()
	accountDir := filepath.Join(root, "account")
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataKeyHex := strings.Repeat("7d", 32)
	dataKey, err := hex.DecodeString(dataKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	plain := createUserVersionPage(t, filepath.Join(root, "plain.db"), 41)
	sourcePath := filepath.Join(messageDir, "message_0.db")
	writeEncryptedMainPage(t, sourcePath, plain, dataKey, []byte("retry-query-salt"))
	client, err := NewClient(accountDir, dataKeyHex, newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	firstToken, err := client.ensureDirectRead(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	client.directMu.Lock()
	snapshot := client.direct[sourcePath].snapshot
	client.directMu.Unlock()
	snapshot.cacheMu.Lock()
	pageOne := snapshot.cache[1]
	if pageOne == nil {
		snapshot.cacheMu.Unlock()
		t.Fatal("validated direct snapshot did not cache page 1")
	}
	pageOne.Value.(*directPageCacheEntry).data = make([]byte, pageSize)
	snapshot.cacheMu.Unlock()

	const readers = 32
	start := make(chan struct{})
	errorsFound := make(chan error, readers)
	var wg sync.WaitGroup
	for reader := 0; reader < readers; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rows, queryErr := client.queryLiveRows(sourcePath, `PRAGMA user_version`)
			if queryErr != nil {
				errorsFound <- queryErr
				return
			}
			if len(rows) != 1 || toInt64(rows[0]["user_version"]) != 41 {
				errorsFound <- fmt.Errorf("live query rows = %#v", rows)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errorsFound)
	for queryErr := range errorsFound {
		t.Error(queryErr)
	}
	if t.Failed() {
		return
	}
	client.directMu.Lock()
	secondToken := client.direct[sourcePath].token
	client.directMu.Unlock()
	if secondToken == firstToken {
		t.Fatal("retry reused the broken direct snapshot")
	}
}

func TestDirectSQLiteReadsOnlyRequestedPagesFromMultiPageDatabase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHATLOG_DIR", filepath.Join(root, "chatlog"))
	accountDir := filepath.Join(root, "account")
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plainPath := filepath.Join(root, "plain-multipage.db")
	const fixtureDriver = "sqlite3-direct-reserve-fixture"
	sql.Register(fixtureDriver, &sqlite3.SQLiteDriver{ConnectHook: func(connection *sqlite3.SQLiteConn) error {
		return connection.SetFileControlInt("main", sqlite3.SQLITE_FCNTL_RESERVE_BYTES, reserveSize)
	}})
	db, err := sql.Open(fixtureDriver, plainPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA page_size=4096; CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT);`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO items(id, value) VALUES (?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatal(err)
	}
	for id := 1; id <= 2500; id++ {
		if _, err := statement.Exec(id, fmt.Sprintf("direct-value-%04d-%s", id, strings.Repeat("x", 80))); err != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			_ = db.Close()
			t.Fatal(err)
		}
	}
	_ = statement.Close()
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	dataKeyHex := strings.Repeat("5b", 32)
	dataKey, err := hex.DecodeString(dataKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(messageDir, "message_0.db")
	writeEncryptedDatabase(t, sourcePath, plainPath, dataKey, []byte("multipage-salt-1"))
	client, err := NewClient(accountDir, dataKeyHex, newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	directPath, err := client.ensureDirectRead(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queryRows(directPath, `SELECT COUNT(*) AS n, MIN(id) AS lo, MAX(id) AS hi FROM items`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || toInt64(rows[0]["n"]) != 2500 || toInt64(rows[0]["lo"]) != 1 || toInt64(rows[0]["hi"]) != 2500 {
		t.Fatalf("multipage aggregate = %#v", rows)
	}
	rows, err = queryRows(directPath, `SELECT value FROM items WHERE id=2379`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !strings.HasPrefix(fmt.Sprint(rows[0]["value"]), "direct-value-2379-") {
		t.Fatalf("multipage lookup = %#v", rows)
	}
}

func createUserVersionPage(t *testing.T, path string, version int) []byte {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA page_size=4096`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, version)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != pageSize {
		t.Fatalf("SQLite fixture size = %d, want one page", len(raw))
	}
	return raw
}

func writeEncryptedMainPage(t *testing.T, path string, plain, dataKey, salt []byte) {
	t.Helper()
	decryptor := decryptdarwin.NewV4Decryptor()
	encKey, _, err := decryptor.DeriveKeys(dataKey, salt)
	if err != nil {
		t.Fatal(err)
	}
	page := make([]byte, pageSize)
	copy(page[:saltSize], salt)
	ivOffset := pageSize - reserveSize
	iv := []byte("main-page-testiv")
	copy(page[ivOffset:ivOffset+aes.BlockSize], iv)
	block, err := aes.NewCipher(encKey)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(page[saltSize:ivOffset], plain[saltSize:ivOffset])
	if err := os.WriteFile(path, page, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeEncryptedDatabase(t *testing.T, destination, plainPath string, dataKey, salt []byte) {
	t.Helper()
	plain, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) == 0 || len(plain)%pageSize != 0 || len(salt) != saltSize {
		t.Fatalf("invalid multipage fixture: bytes=%d salt=%d", len(plain), len(salt))
	}
	decryptor := decryptdarwin.NewV4Decryptor()
	encKey, _, err := decryptor.DeriveKeys(dataKey, salt)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, len(plain))
	block, err := aes.NewCipher(encKey)
	if err != nil {
		t.Fatal(err)
	}
	for pageIndex := 0; pageIndex < len(plain)/pageSize; pageIndex++ {
		plainPage := plain[pageIndex*pageSize : (pageIndex+1)*pageSize]
		page := encrypted[pageIndex*pageSize : (pageIndex+1)*pageSize]
		iv := []byte(fmt.Sprintf("direct-page-%04d", pageIndex+1))
		if len(iv) != aes.BlockSize {
			t.Fatalf("fixture IV length = %d", len(iv))
		}
		ivOffset := pageSize - reserveSize
		copy(page[ivOffset:ivOffset+aes.BlockSize], iv)
		if pageIndex == 0 {
			copy(page[:saltSize], salt)
			cipher.NewCBCEncrypter(block, iv).CryptBlocks(page[saltSize:ivOffset], plainPage[saltSize:ivOffset])
		} else {
			cipher.NewCBCEncrypter(block, iv).CryptBlocks(page[:ivOffset], plainPage[:ivOffset])
		}
	}
	if err := os.WriteFile(destination, encrypted, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSQLiteUserVersion(t *testing.T, path string, want int64) {
	t.Helper()
	rows, err := queryRows(path, `PRAGMA user_version`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || toInt64(rows[0]["user_version"]) != want {
		t.Fatalf("user_version rows = %#v, want %d", rows, want)
	}
}

func TestDeriveDBEncryptionKeyUsesDatabaseSalt(t *testing.T) {
	key := bytesOf(0x23, 32)
	salt := []byte("database-salt-01")
	dbPath := filepath.Join(t.TempDir(), "encrypted.db")
	if err := os.WriteFile(dbPath, append(append([]byte(nil), salt...), make([]byte, pageSize-len(salt))...), 0o600); err != nil {
		t.Fatal(err)
	}
	decryptor := decryptdarwin.NewV4Decryptor()
	want, _, err := decryptor.DeriveKeys(key, salt)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deriveDBEncryptionKey(decryptor, dbPath, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("derived WAL page key does not match database encryption key")
	}
}

func appendWALFrame(t *testing.T, wal, key []byte, pageNo, dbSize uint32, plain []byte) []byte {
	t.Helper()
	if len(wal) < walHeaderSize {
		t.Fatalf("WAL header size = %d", len(wal))
	}
	if binary.BigEndian.Uint32(wal[0:4]) == 0 {
		binary.BigEndian.PutUint32(wal[0:4], 0x377f0682)
		binary.BigEndian.PutUint32(wal[4:8], 3007000)
	}
	if binary.BigEndian.Uint32(wal[8:12]) == 0 {
		binary.BigEndian.PutUint32(wal[8:12], pageSize)
	}
	headerSum1, headerSum2 := directWALChecksum(wal[:24], true, 0, 0)
	binary.BigEndian.PutUint32(wal[24:28], headerSum1)
	binary.BigEndian.PutUint32(wal[28:32], headerSum2)
	seed1, seed2 := headerSum1, headerSum2
	frameSize := walFrameHdr + pageSize
	if len(wal) >= walHeaderSize+frameSize {
		lastHeader := wal[len(wal)-frameSize : len(wal)-pageSize]
		seed1 = binary.BigEndian.Uint32(lastHeader[16:20])
		seed2 = binary.BigEndian.Uint32(lastHeader[20:24])
	}
	header := make([]byte, walFrameHdr)
	binary.BigEndian.PutUint32(header[0:4], pageNo)
	binary.BigEndian.PutUint32(header[4:8], dbSize)
	copy(header[8:16], wal[16:24])
	encrypted := encryptWALPage(t, key, plain)
	checksum1, checksum2 := directWALChecksum(header[:8], true, seed1, seed2)
	checksum1, checksum2 = directWALChecksum(encrypted, true, checksum1, checksum2)
	binary.BigEndian.PutUint32(header[16:20], checksum1)
	binary.BigEndian.PutUint32(header[20:24], checksum2)
	wal = append(wal, header...)
	return append(wal, encrypted...)
}

func encryptWALPage(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	if len(plain) != pageSize {
		t.Fatalf("plain page size = %d", len(plain))
	}
	page := make([]byte, pageSize)
	iv := []byte("test-wal-page-iv")
	copy(page[pageSize-reserveSize:pageSize-reserveSize+aes.BlockSize], iv)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(
		page[:pageSize-reserveSize],
		plain[:pageSize-reserveSize],
	)
	return page
}

func bytesOf(value byte, size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = value
	}
	return out
}

func createPlainSQLiteDB(t *testing.T, path, schema string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryRowsLimitedStopsAtConfiguredCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rows.db")
	createPlainSQLiteDB(t, path, `
		CREATE TABLE rows_fixture (id INTEGER);
		INSERT INTO rows_fixture VALUES (1), (2), (3), (4), (5), (6);
	`)

	rows, err := queryRowsLimited(path, `SELECT id FROM rows_fixture ORDER BY id`, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("row count = %d, want 3", len(rows))
	}
	if got := toInt64(rows[2]["id"]); got != 3 {
		t.Fatalf("last id = %d, want 3", got)
	}
}

func TestStreamQueryRowsVisitsBeyondInspectionCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream_rows.db")
	createPlainSQLiteDB(t, path, `
		CREATE TABLE rows_fixture (id INTEGER);
		WITH RECURSIVE seq(id) AS (
			SELECT 1
			UNION ALL
			SELECT id + 1 FROM seq WHERE id < 10005
		)
		INSERT INTO rows_fixture SELECT id FROM seq;
	`)

	var lastID int64
	count, err := streamQueryRows(context.Background(), path, `SELECT id FROM rows_fixture ORDER BY id`, func(columns []string, values []interface{}) error {
		if len(columns) != 1 || columns[0] != "id" {
			t.Fatalf("columns = %v", columns)
		}
		lastID = toInt64(values[0])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 10005 || lastID != 10005 {
		t.Fatalf("stream count=%d last_id=%d", count, lastID)
	}
}

func TestResolveDataKeyFallsBackToAnotherVerifiedSavedKey(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	correctKey := strings.Repeat("44", 32)
	wrongKey := strings.Repeat("55", 32)
	dbPath := filepath.Join(messageDir, "weclaw.db")
	writeWCDBEncryptedTestPage(t, dbPath, correctKey, []byte("weclaw--salt-002"))

	allKeys := map[string]any{
		// The path-specific mapping is stale, while another saved DB carries
		// the correct candidate. resolveDataKey must validate and recover.
		"message/weclaw.db":    map[string]string{"enc_key": wrongKey},
		"message/message_0.db": map[string]string{"enc_key": correctKey},
	}
	content, err := json.Marshal(allKeys)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(accountDir, "all_keys.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(accountDir, "", newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := client.resolveDataKey(dbPath)
	if err != nil {
		t.Fatalf("resolveDataKey: %v", err)
	}
	if resolved != correctKey {
		t.Fatalf("resolved key = %q, want verified fallback", resolved)
	}
	// The second call must use the validated cache and remain stable.
	resolvedAgain, err := client.resolveDataKey(dbPath)
	if err != nil || resolvedAgain != correctKey {
		t.Fatalf("cached resolution = %q, %v", resolvedAgain, err)
	}
}

func TestNewClientRecoversMissingEmptyDatabaseMapping(t *testing.T) {
	accountDir := t.TempDir()
	messageDir := filepath.Join(accountDir, "db_storage", "message")
	if err := os.MkdirAll(messageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("66", 32)
	dbPath := filepath.Join(messageDir, "weclaw.db")
	writeWCDBEncryptedTestPage(t, dbPath, key, []byte("weclaw--salt-003"))
	content, err := json.Marshal(map[string]any{
		"message/message_0.db": map[string]string{"enc_key": key},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(accountDir, "all_keys.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(accountDir, "", newTestDecryptor)
	if err != nil {
		t.Fatal(err)
	}
	if client.allKeys["message/weclaw.db"] != key {
		t.Fatalf("missing DB mapping was not recovered: %#v", client.allKeys)
	}
	found := false
	for _, path := range client.ListAllKeyDBs() {
		if filepath.Clean(path) == filepath.Clean(dbPath) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("recovered DB is absent from ListAllKeyDBs")
	}
}

func TestResolvePathWithinDataDir(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "db_storage")
	inside := filepath.Join(dataDir, "message", "message_0.db")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, requested := range []string{
		inside,
		"message/message_0.db",
		"db_storage/message/message_0.db",
	} {
		got, err := resolvePathWithinDataDir(dataDir, requested)
		if err != nil {
			t.Fatalf("resolvePathWithinDataDir(%q): %v", requested, err)
		}
		if filepath.Clean(got) != filepath.Clean(inside) {
			t.Fatalf("resolvePathWithinDataDir(%q) = %q, want %q", requested, got, inside)
		}
	}
}

func TestResolvePathWithinDataDirRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "db_storage")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.db")
	if err := os.WriteFile(outside, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, requested := range []string{outside, "../outside.db"} {
		if got, err := resolvePathWithinDataDir(dataDir, requested); err == nil {
			t.Fatalf("resolvePathWithinDataDir(%q) accepted %q", requested, got)
		}
	}

	link := filepath.Join(dataDir, "linked.db")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if got, err := resolvePathWithinDataDir(dataDir, link); err == nil {
		t.Fatalf("symlink escape accepted as %q", got)
	}
}

func TestListAllKeyDBsOmitsPathsOutsideDataDirectory(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "db_storage")
	if err := os.MkdirAll(filepath.Join(dataDir, "message"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dataDir, "message", "message_0.db")
	outside := filepath.Join(root, "outside.db")
	if err := os.WriteFile(inside, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		dataDir: dataDir,
		allKeys: map[string]string{
			"message/message_0.db": strings.Repeat("11", 32),
			"../outside.db":        strings.Repeat("11", 32),
		},
	}
	got := client.ListAllKeyDBs()
	if len(got) != 1 || filepath.Clean(got[0]) != filepath.Clean(inside) {
		t.Fatalf("ListAllKeyDBs = %v, want only %q", got, inside)
	}
}

func writeWCDBEncryptedTestPage(t *testing.T, path, keyHex string, salt []byte) {
	t.Helper()
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatal(err)
	}
	decryptor := decryptdarwin.NewV4Decryptor()
	encKey, _, err := decryptor.DeriveKeys(key, salt)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, decryptor.GetPageSize())
	copy(plain, []byte("SQLite format 3\x00"))
	binary.BigEndian.PutUint16(plain[16:18], uint16(decryptor.GetPageSize()))
	plain[18], plain[19], plain[20] = 1, 1, byte(decryptor.GetReserve())
	plain[21], plain[22], plain[23] = 64, 32, 32
	// Exercise WeChat's initialized-but-empty DB header (weclaw/solitaire):
	// encoding and schema format remain zero until the first table is created.
	binary.BigEndian.PutUint32(plain[56:60], 0)
	plain[100] = 13

	page := make([]byte, decryptor.GetPageSize())
	copy(page[:16], salt)
	ivOffset := len(page) - decryptor.GetReserve()
	iv := []byte("test-page-one-iv")
	copy(page[ivOffset:ivOffset+aes.BlockSize], iv)
	block, err := aes.NewCipher(encKey)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(page[16:ivOffset], plain[16:ivOffset])
	if err := os.WriteFile(path, page, 0o600); err != nil {
		t.Fatal(err)
	}
}
