package observability

import (
	"io"
	"strings"
	"sync"
	"testing"
)

func TestLogWriterParsesClassifiesAndBoundsEntries(t *testing.T) {
	store := NewLogStore(2)
	writer := &logLineWriter{store: store}
	input := strings.Join([]string{
		`{"level":"info","time":"2026-07-30T00:00:00Z","message":"Starting HTTP server"}`,
		`{"level":"warn","message":"wcdb query slowed down"}`,
		`{"level":"error","message":"media cache failed","error":"disk full"}`,
	}, "\n") + "\n"
	if _, err := io.WriteString(writer, input); err != nil {
		t.Fatal(err)
	}
	if store.Len() != 2 {
		t.Fatalf("entries = %d, want 2", store.Len())
	}
	database := store.Recent("database", "warn", 10)
	if len(database) != 1 || database[0].Message != "wcdb query slowed down" {
		t.Fatalf("database logs = %+v", database)
	}
	cache := store.Recent("cache", "error", 10)
	if len(cache) != 1 || !strings.Contains(cache[0].Message, "disk full") {
		t.Fatalf("cache logs = %+v", cache)
	}
}

func TestLogStoreReturnsNewestEntriesFirst(t *testing.T) {
	store := NewLogStore(5)
	store.Add(LogEntry{Message: "first"})
	store.Add(LogEntry{Message: "second"})
	entries := store.Recent("all", "all", 5)
	if len(entries) != 2 || entries[0].Message != "second" || entries[1].Message != "first" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestSharedMessageChangeWarningsAreDatabaseLogs(t *testing.T) {
	store := NewLogStore(5)
	writer := &logLineWriter{store: store}
	_, err := io.WriteString(writer, `{"level":"warn","message":"shared message change scan incomplete","error":"automatic extension loading failed"}`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	entries := store.Recent("database", "warn", 5)
	if len(entries) != 1 {
		t.Fatalf("database warning entries = %+v", entries)
	}
}

func TestLogStoreSupportsConcurrentReadersAndWriters(t *testing.T) {
	store := NewLogStore(100)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < 200; index++ {
				store.Add(LogEntry{Category: "http", Message: "request"})
				_ = store.Recent("http", "all", 20)
				_ = store.CategoryCounts()
			}
		}()
	}
	wait.Wait()
	if store.Len() != 100 {
		t.Fatalf("entries = %d, want 100", store.Len())
	}
}
