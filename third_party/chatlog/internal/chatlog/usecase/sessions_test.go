package usecase

import (
	"testing"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

type sessionStateFixture struct {
	last time.Time
}

func (s *sessionStateFixture) SetLastSession(value time.Time) {
	s.last = value
}

type sessionDatabaseFixture struct {
	ready      bool
	startCalls int
	queryCalls int
	items      []*model.Session
}

func (d *sessionDatabaseFixture) Start() error {
	d.startCalls++
	d.ready = true
	return nil
}
func (d *sessionDatabaseFixture) Ready() bool {
	return d.ready
}
func (d *sessionDatabaseFixture) GetSessions(string, int, int) (*ports.Sessions, error) {
	d.queryCalls++
	return &ports.Sessions{Items: d.items}, nil
}

func TestSessionsStartsDatabaseAndUpdatesState(t *testing.T) {
	want := time.Unix(1_725_000_000, 0)
	state := &sessionStateFixture{}
	db := &sessionDatabaseFixture{items: []*model.Session{{NTime: want}}}
	sessions := NewSessions(state, db)

	if err := sessions.Refresh(); err != nil {
		t.Fatal(err)
	}
	if db.startCalls != 1 {
		t.Fatalf("database starts = %d", db.startCalls)
	}
	if !state.last.Equal(want) {
		t.Fatalf("last session = %s, want %s", state.last, want)
	}
	if db.queryCalls != 1 {
		t.Fatalf("refresh queries = %d, want 1", db.queryCalls)
	}
	latest, err := sessions.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || !latest.NTime.Equal(want) {
		t.Fatalf("latest session = %#v", latest)
	}
}

func TestSessionsRefreshLatestReusesWatermarkQueryResult(t *testing.T) {
	want := &model.Session{UserName: "wxid_fixture", NTime: time.Unix(1_725_000_123, 0)}
	state := &sessionStateFixture{}
	db := &sessionDatabaseFixture{ready: true, items: []*model.Session{want}}
	sessions := NewSessions(state, db)

	latest, err := sessions.RefreshLatest()
	if err != nil {
		t.Fatal(err)
	}
	if latest != want || db.queryCalls != 1 || !state.last.Equal(want.NTime) {
		t.Fatalf("latest=%#v queries=%d watermark=%s", latest, db.queryCalls, state.last)
	}
}
