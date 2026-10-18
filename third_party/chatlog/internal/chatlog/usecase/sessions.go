package usecase

import (
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

type SessionDatabase interface {
	Start() error
	Ready() bool
	GetSessions(key string, limit, offset int) (*ports.Sessions, error)
}

type SessionState interface {
	SetLastSession(value time.Time)
}

type Sessions struct {
	state SessionState
	db    SessionDatabase
}

func NewSessions(state SessionState, db SessionDatabase) *Sessions {
	return &Sessions{state: state, db: db}
}

func (u *Sessions) Refresh() error {
	_, err := u.RefreshLatest()
	return err
}

// RefreshLatest updates the session watermark and returns the same row used for
// that update. TUI callers use it to refresh both the status bar and footer
// without issuing two identical SessionTable queries.
func (u *Sessions) RefreshLatest() (*model.Session, error) {
	if !u.db.Ready() {
		if err := u.db.Start(); err != nil {
			return nil, err
		}
	}
	response, err := u.db.GetSessions("", 1, 0)
	if err != nil {
		return nil, err
	}
	if len(response.Items) > 0 {
		latest := response.Items[0]
		u.state.SetLastSession(latest.NTime)
		return latest, nil
	}
	return nil, nil
}

func (u *Sessions) Latest() (*model.Session, error) {
	if u.db == nil || !u.db.Ready() {
		return nil, nil
	}
	response, err := u.db.GetSessions("", 1, 0)
	if err != nil {
		return nil, err
	}
	if len(response.Items) == 0 {
		return nil, nil
	}
	return response.Items[0], nil
}
