//go:build sqlite_fts5 || fts5

package wcdbapi

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestSQLiteFTS5BuildFeature(t *testing.T) {
	if code := MMFTSTokenizerRegistrationCode(); code != 0 {
		t.Fatalf("register MMFtsTokenizer auto extension: SQLite code %d", code)
	}
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var enabled int
	if err := db.QueryRow("SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatal("SQLite ENABLE_FTS5 compile option is missing")
	}
	if _, err := db.Exec("CREATE VIRTUAL TABLE search_probe USING fts5(content)"); err != nil {
		t.Fatalf("create FTS5 table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO search_probe(content) VALUES ('chatlog fts5 probe')"); err != nil {
		t.Fatalf("insert FTS5 row: %v", err)
	}
	var content string
	if err := db.QueryRow(
		"SELECT content FROM search_probe WHERE search_probe MATCH 'fts5'",
	).Scan(&content); err != nil {
		t.Fatalf("query FTS5 table: %v", err)
	}
	if content != "chatlog fts5 probe" {
		t.Fatalf("unexpected FTS5 content: %q", content)
	}

	if _, err := db.Exec(
		"CREATE VIRTUAL TABLE mm_search_probe USING fts5(tokenize = 'MMFtsTokenizer disable_pinyin', content)",
	); err != nil {
		t.Fatalf("create MMFtsTokenizer table: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO mm_search_probe(content) VALUES ('微信聊天 running quickly')",
	); err != nil {
		t.Fatalf("insert MMFtsTokenizer row: %v", err)
	}
	for _, query := range []string{`"微信"`, "run"} {
		var count int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM mm_search_probe WHERE mm_search_probe MATCH ?",
			query,
		).Scan(&count); err != nil {
			t.Fatalf("query MMFtsTokenizer table with %q: %v", query, err)
		}
		if count != 1 {
			t.Fatalf("MMFtsTokenizer query %q returned %d rows", query, count)
		}
	}
}

func TestMMFTSTokenizerAutoExtensionConcurrentOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-mmfts.db")
	seed, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(
		"CREATE VIRTUAL TABLE mm_search_probe USING fts5(tokenize = 'MMFtsTokenizer disable_pinyin', content)",
	); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if _, err := seed.Exec("INSERT INTO mm_search_probe(content) VALUES ('微信聊天')"); err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	const readers = 256
	errs := make(chan error, readers)
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := 0; index < readers; index++ {
		go func() {
			defer wait.Done()
			db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_query_only=1", path))
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			var count int
			if err := db.QueryRow(
				"SELECT COUNT(*) FROM mm_search_probe WHERE mm_search_probe MATCH '微信'",
			).Scan(&count); err != nil {
				errs <- err
				return
			}
			if count != 1 {
				errs <- fmt.Errorf("unexpected count %d", count)
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent SQLite open: %v", err)
	}
}
