package database

import (
	"context"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

// HookDatabase is the narrow read-and-watch capability needed by the message
// hook runtime. It keeps the database module independent from its concrete
// delivery implementation.
type HookDatabase interface {
	GetSessions(key string, limit, offset int) (*ports.Sessions, error)
	GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error)
	GetMessages(start, end time.Time, talker, sender, keyword string, limit, offset int) ([]*model.Message, error)
	GetMessage(talker string, seq int64) (*model.Message, error)
	GetMedia(mediaType, key string) (*model.Media, error)
	SetCallback(group string, callback func(event fsnotify.Event) error) error
}

type HookRuntime interface {
	Run(ctx context.Context)
	Close() error
	Wake()
	RecentEvents(limit int) ([]ports.HookEvent, error)
	ClearEvents() (int, error)
	CancelEvent(eventID, target string) (int64, error)
	RetryEvent(eventID, target string) (int64, error)
	Stats() (ports.HookRuntimeStats, error)
	StorePath() string
}

type HookFactory func(conf Config, db HookDatabase) (HookRuntime, error)
