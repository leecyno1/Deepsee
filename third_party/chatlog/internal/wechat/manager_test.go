package wechat

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/sjzar/chatlog/internal/wechat/decrypt"
	"github.com/sjzar/chatlog/internal/wechat/key"
	"github.com/sjzar/chatlog/internal/wechat/model"
	"github.com/sjzar/chatlog/internal/wechat/process"
)

type detectorFixture struct {
	processes []*model.Process
}

func (d detectorFixture) FindProcesses() ([]*model.Process, error) {
	return d.processes, nil
}

type extractorFixture struct {
	key string
}

func (e extractorFixture) Extract(context.Context, *model.Process) (string, string, error) {
	return e.key, "", nil
}

func (e extractorFixture) SearchKey(context.Context, []byte) (string, bool) {
	return "", false
}

type backendFixture struct {
	detector process.Detector
	key      string
}

func (b backendFixture) ID() string {
	return "fixture"
}

func (b backendFixture) Detector() process.Detector {
	return b.detector
}

func (b backendFixture) NewExtractor(int) (key.Extractor, error) {
	return extractorFixture{key: b.key}, nil
}

func (b backendFixture) NewDecryptor(int) (decrypt.Decryptor, error) {
	return nil, stderrors.New("not used")
}

func TestManagersKeepInjectedBackendsIsolated(t *testing.T) {
	first := NewManager(backendFixture{
		detector: detectorFixture{processes: []*model.Process{{
			PID: 1, AccountName: "alpha", Version: 4, DataDir: "/fixture/alpha",
		}}},
		key: "alpha-key",
	})
	second := NewManager(backendFixture{
		detector: detectorFixture{processes: []*model.Process{{
			PID: 2, AccountName: "beta", Version: 4, DataDir: "/fixture/beta",
		}}},
		key: "beta-key",
	})

	for _, manager := range []*Manager{first, second} {
		if err := manager.Load(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.GetAccount("beta"); err == nil {
		t.Fatal("first manager observed the second manager's account")
	}
	account, err := first.GetAccount("alpha")
	if err != nil {
		t.Fatal(err)
	}
	dataKey, _, err := account.GetKey(key.WithRequest(context.Background(), key.Request{DataOnly: true}))
	if err != nil {
		t.Fatal(err)
	}
	if dataKey != "alpha-key" {
		t.Fatalf("data key = %q, want injected backend result", dataKey)
	}
}
