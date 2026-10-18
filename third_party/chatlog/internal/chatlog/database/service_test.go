package database

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/wechatdb"
)

type lifecycleConfigFixture struct{}

func (lifecycleConfigFixture) GetWorkDir() string                { return "" }
func (lifecycleConfigFixture) GetDataDir() string                { return "" }
func (lifecycleConfigFixture) GetDataKey() string                { return "" }
func (lifecycleConfigFixture) GetVersion() int                   { return 4 }
func (lifecycleConfigFixture) GetRuntimeDir() string             { return "" }
func (lifecycleConfigFixture) GetHTTPAddr() string               { return "127.0.0.1:0" }
func (lifecycleConfigFixture) GetMessageHook() *conf.MessageHook { return &conf.MessageHook{} }
func (lifecycleConfigFixture) GetAccount() string                { return "" }

func TestStartIsIdempotentAndRestartable(t *testing.T) {
	var opens atomic.Int32
	openDB := func(string, string) (*wechatdb.DB, error) {
		opens.Add(1)
		return &wechatdb.DB{}, nil
	}
	s := NewService(lifecycleConfigFixture{}, openDB, nil)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Start(); err != nil {
				t.Errorf("start failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := opens.Load(); got != 1 {
		t.Fatalf("database opened %d times, want 1", got)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if got := opens.Load(); got != 2 {
		t.Fatalf("database open count after restart = %d, want 2", got)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
}
