package wechatdb

import (
	"context"
	"fmt"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "github.com/mattn/go-sqlite3"

	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechatdb/datasource"
	"github.com/sjzar/chatlog/internal/wechatdb/repository"
)

type DB struct {
	ds   datasource.DataSource
	repo *repository.Repository
}

func New(ds datasource.DataSource) (*DB, error) {
	w := &DB{ds: ds}
	repo, err := repository.New(ds)
	if err != nil {
		_ = ds.Close()
		return nil, err
	}
	w.repo = repo
	return w, nil
}

func (w *DB) Close() error {
	if w.repo != nil {
		return w.repo.Close()
	}
	return nil
}

func (w *DB) GetMessages(start, end time.Time, talker string, sender string, keyword string, limit, offset int) ([]*model.Message, error) {
	ctx := context.Background()

	// 使用 repository 获取消息
	messages, err := w.repo.GetMessages(ctx, start, end, talker, sender, keyword, limit, offset)
	if err != nil {
		return nil, err
	}

	return messages, nil
}

func (w *DB) GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error) {
	return w.repo.GetMessagesAfter(context.Background(), talker, cursor, limit)
}

func (w *DB) ResolveChangedMessageTalkers(
	changedFiles []string,
	candidates map[string]model.MessageCursor,
) ([]string, error) {
	return w.repo.ResolveChangedMessageTalkers(context.Background(), changedFiles, candidates)
}

func (w *DB) GetMessagesAfterFiles(
	talker string,
	cursor model.MessageCursor,
	limit int,
	changedFiles []string,
) ([]*model.Message, error) {
	return w.repo.GetMessagesAfterFiles(context.Background(), talker, cursor, limit, changedFiles)
}

type messageSearchDataSource interface {
	SearchMessages(
		ctx context.Context,
		startTime, endTime time.Time,
		talkers []string,
		keyword string,
		messageType int64,
		limit, offset int,
	) ([]*model.Message, int, string, error)
}

type advancedMessageSearchDataSource interface {
	SearchMessagesAdvanced(context.Context, model.MessageSearchRequest) (model.MessageSearchResult, error)
}

type messageAnalyticsDataSource interface {
	AnalyzeMessages(context.Context, model.MessageAnalyticsRequest) (model.MessageAnalyticsResult, error)
}

func (w *DB) SearchMessages(
	ctx context.Context,
	startTime, endTime time.Time,
	talkers []string,
	keyword string,
	messageType int64,
	limit, offset int,
) ([]*model.Message, int, string, error) {
	searcher, ok := w.ds.(messageSearchDataSource)
	if !ok {
		return nil, 0, "", fmt.Errorf("message search backend is not configured")
	}
	talkers = w.repo.ResolveTalkers(ctx, talkers)
	messages, total, path, err := searcher.SearchMessages(
		ctx, startTime, endTime, talkers, keyword, messageType, limit, offset,
	)
	if err != nil {
		return nil, 0, path, err
	}
	if err := w.repo.EnrichMessages(ctx, messages); err != nil {
		return nil, 0, path, err
	}
	return messages, total, path, nil
}

func (w *DB) SearchMessagesAdvanced(
	ctx context.Context,
	request model.MessageSearchRequest,
) (model.MessageSearchResult, error) {
	searcher, ok := w.ds.(advancedMessageSearchDataSource)
	if !ok {
		messages, total, path, err := w.SearchMessages(
			ctx,
			request.StartTime,
			request.EndTime,
			request.Talkers,
			request.Keyword,
			request.MessageType,
			request.Limit,
			request.Offset,
		)
		return model.MessageSearchResult{
			Messages: messages,
			Total:    total,
			Path:     path,
			Match:    "phrase",
			Sort:     "time",
		}, err
	}
	request.Talkers = w.repo.ResolveTalkers(ctx, request.Talkers)
	result, err := searcher.SearchMessagesAdvanced(ctx, request)
	if err != nil {
		return result, err
	}
	if err := w.repo.EnrichMessages(ctx, result.Messages); err != nil {
		return result, err
	}
	return result, nil
}

func (w *DB) AnalyzeMessages(
	ctx context.Context,
	request model.MessageAnalyticsRequest,
) (model.MessageAnalyticsResult, error) {
	analyzer, ok := w.ds.(messageAnalyticsDataSource)
	if !ok {
		return model.MessageAnalyticsResult{}, fmt.Errorf("message analytics backend is not configured")
	}
	request.Talkers = w.repo.ResolveTalkers(ctx, request.Talkers)
	return analyzer.AnalyzeMessages(ctx, request)
}

func (w *DB) GetMessage(talker string, seq int64) (*model.Message, error) {
	return w.repo.GetMessage(context.Background(), talker, seq)
}

type GetContactsResp struct {
	Items []*model.Contact `json:"items"`
}

func (w *DB) GetContacts(key string, limit, offset int) (*GetContactsResp, error) {
	ctx := context.Background()

	contacts, err := w.repo.GetContacts(ctx, key, limit, offset)
	if err != nil {
		return nil, err
	}

	return &GetContactsResp{
		Items: contacts,
	}, nil
}

func (w *DB) GetContact(key string) (*model.Contact, error) {
	return w.repo.GetContact(context.Background(), key)
}

type GetChatRoomsResp struct {
	Items []*model.ChatRoom `json:"items"`
}

func (w *DB) GetChatRooms(key string, limit, offset int) (*GetChatRoomsResp, error) {
	ctx := context.Background()

	chatRooms, err := w.repo.GetChatRooms(ctx, key, limit, offset)
	if err != nil {
		return nil, err
	}

	return &GetChatRoomsResp{
		Items: chatRooms,
	}, nil
}

func (w *DB) GetChatRoom(key string) (*model.ChatRoom, error) {
	return w.repo.GetChatRoom(context.Background(), key)
}

type GetSessionsResp struct {
	Items []*model.Session `json:"items"`
}

func (w *DB) GetSessions(key string, limit, offset int) (*GetSessionsResp, error) {
	ctx := context.Background()

	// 使用 repository 获取会话列表
	sessions, err := w.repo.GetSessions(ctx, key, limit, offset)
	if err != nil {
		return nil, err
	}

	return &GetSessionsResp{
		Items: sessions,
	}, nil
}

func (w *DB) GetMedia(_type string, key string) (*model.Media, error) {
	return w.repo.GetMedia(context.Background(), _type, key)
}

func (w *DB) GetMediaByName(_type string, name string, size int64) (*model.Media, error) {
	return w.repo.GetMediaByName(context.Background(), _type, name, size)
}

func (w *DB) SetCallback(group string, callback func(event fsnotify.Event) error) error {
	return w.ds.SetCallback(group, callback)
}

// SubscribeCallback is the cancellable form used by feature runtimes whose
// lifetime is shorter than the database itself.
func (w *DB) SubscribeCallback(group string, callback func(event fsnotify.Event) error) (func(), error) {
	subscriber, ok := w.ds.(interface {
		SubscribeCallback(string, func(fsnotify.Event) error) (func(), error)
	})
	if !ok {
		return nil, fmt.Errorf("database datasource does not support change subscriptions")
	}
	return subscriber.SubscribeCallback(group, callback)
}

func (w *DB) GetDBs() (map[string][]string, error) {
	return w.ds.GetDBs()
}

func (w *DB) GetTables(group, file string) ([]string, error) {
	return w.ds.GetTables(group, file)
}

func (w *DB) GetTableData(group, file, table string, limit, offset int, keyword string) ([]map[string]interface{}, error) {
	return w.ds.GetTableData(group, file, table, limit, offset, keyword)
}

func (w *DB) StreamTableData(
	ctx context.Context,
	group string,
	file string,
	table string,
	keyword string,
	visit func(columns []string, values []interface{}) error,
) (int64, error) {
	return w.ds.StreamTableData(ctx, group, file, table, keyword, visit)
}

func (w *DB) ExecuteSQL(group, file, query string) ([]map[string]interface{}, error) {
	return w.ds.ExecuteSQL(group, file, query)
}

func (w *DB) StreamSQL(
	ctx context.Context,
	group string,
	file string,
	query string,
	visit func(columns []string, values []interface{}) error,
) (int64, error) {
	return w.ds.StreamSQL(ctx, group, file, query, visit)
}

func (w *DB) SearchAll(keyword string, limit int, deep bool) ([]map[string]interface{}, error) {
	return w.ds.SearchAll(keyword, limit, deep)
}

type detailedDatabaseSearchDataSource interface {
	SearchAllDetailed(context.Context, model.DatabaseSearchRequest) (model.DatabaseSearchResult, error)
}

func (w *DB) SearchAllDetailed(
	ctx context.Context,
	request model.DatabaseSearchRequest,
) (model.DatabaseSearchResult, error) {
	searcher, ok := w.ds.(detailedDatabaseSearchDataSource)
	if !ok {
		items, err := w.ds.SearchAll(request.Keyword, request.Limit, request.Mode == "deep")
		return model.DatabaseSearchResult{Items: items}, err
	}
	return searcher.SearchAllDetailed(ctx, request)
}

// GetSNSTimeline 获取朋友圈时间线数据
func (w *DB) GetSNSTimeline(username string, limit, offset int) ([]map[string]interface{}, error) {
	return w.ds.GetSNSTimeline(context.Background(), username, limit, offset)
}

// GetSNSCount 获取朋友圈数量统计
func (w *DB) GetSNSCount(username string) (int, error) {
	return w.ds.GetSNSCount(context.Background(), username)
}
