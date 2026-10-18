package http

import (
	"context"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

type DatabaseStatus interface {
	Status() (state ports.DatabaseState, message string)
	Ready() bool
}

// DatabaseChangeSubscriber is optional for alternate adapters. The production
// WCDB service supports subscriptions before database startup and rebinds them
// across database restarts.
type DatabaseChangeSubscriber interface {
	SubscribeCallback(group string, callback func(fsnotify.Event) error) (func(), error)
}

type MessageChangeStatusProvider interface {
	MessageChangeStats() ports.MessageChangeStats
}

type ChatQuery interface {
	GetMessages(start, end time.Time, talker, sender, keyword string, limit, offset int) ([]*model.Message, error)
	GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error)
	GetSessions(key string, limit, offset int) (*ports.Sessions, error)
}

// MessageSearchQuery is an optional optimized search capability. Keeping it
// separate from ChatQuery lets alternate database adapters retain the ordinary
// message-table fallback.
type MessageSearchQuery interface {
	SearchMessages(
		ctx context.Context,
		startTime, endTime time.Time,
		talkers []string,
		keyword string,
		messageType int64,
		limit, offset int,
	) ([]*model.Message, int, string, error)
}

type AdvancedMessageSearchQuery interface {
	SearchMessagesAdvanced(context.Context, model.MessageSearchRequest) (model.MessageSearchResult, error)
}

type MessageAnalyticsQuery interface {
	AnalyzeMessages(context.Context, model.MessageAnalyticsRequest) (model.MessageAnalyticsResult, error)
}

type AddressBookQuery interface {
	GetContacts(key string, limit, offset int) (*ports.Contacts, error)
	GetContact(key string) (*model.Contact, error)
	GetChatRooms(key string, limit, offset int) (*ports.ChatRooms, error)
	GetChatRoom(key string) (*model.ChatRoom, error)
}

type MediaQuery interface {
	GetMedia(mediaType, key string) (*model.Media, error)
	GetMediaByName(mediaType, name string, size int64) (*model.Media, error)
}

type DatabaseInspector interface {
	GetDecryptedDBs() (map[string][]string, error)
	GetTables(group, file string) ([]string, error)
	GetTableData(group, file, table string, limit, offset int, keyword string) ([]map[string]interface{}, error)
	StreamTableData(ctx context.Context, group, file, table, keyword string, visit func(columns []string, values []interface{}) error) (int64, error)
	ExecuteSQL(group, file, query string) ([]map[string]interface{}, error)
	StreamSQL(ctx context.Context, group, file, query string, visit func(columns []string, values []interface{}) error) (int64, error)
	SearchAll(keyword string, limit int, deep bool) ([]map[string]interface{}, error)
	ClearDecryptedCache() (files int64, bytes int64, err error)
}

type DetailedDatabaseSearch interface {
	SearchAllDetailed(context.Context, model.DatabaseSearchRequest) (model.DatabaseSearchResult, error)
}

type SocialQuery interface {
	GetSNSTimeline(username string, limit, offset int) ([]map[string]interface{}, error)
}

type HookAdmin interface {
	WakeMessageHook()
	GetMessageHookEvents(limit int) ([]ports.HookEvent, error)
	ClearMessageHookEvents() (int, error)
	CancelMessageHookEvent(eventID, target string) (int64, error)
	RetryMessageHookEvent(eventID, target string) (int64, error)
	GetMessageHookStats() (ports.HookRuntimeStats, error)
	MessageHookStorePath() string
}

// Database composes focused ports. Individual handlers and fixtures can use
// the narrow capability interfaces above.
type Database interface {
	DatabaseStatus
	ChatQuery
	AddressBookQuery
	MediaQuery
	DatabaseInspector
	SocialQuery
	HookAdmin
}
