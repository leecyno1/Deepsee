package database

import (
	"context"
	stderrors "errors"
	"fmt"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechatdb"
	"github.com/sjzar/chatlog/internal/wechatdb/wcdbapi"
	"github.com/sjzar/chatlog/pkg/util"
)

const (
	StateInit    = ports.DatabaseStateInit
	StateOpening = ports.DatabaseStateOpening
	StateReady   = ports.DatabaseStateReady
	StateError   = ports.DatabaseStateError
)

type Service struct {
	State      ports.DatabaseState
	StateMsg   string
	conf       Config
	db         *wechatdb.DB
	hookSvc    HookRuntime
	hookCancel context.CancelFunc
	changeFeed *messageChangeCoordinator
	openDB     OpenDB
	openHook   HookFactory
	lifecycle  sync.Mutex
	changeMu   sync.RWMutex

	subscriptionMu     sync.Mutex
	subscriptions      map[uint64]*changeSubscription
	nextSubscriptionID uint64
}

var _ ports.MessageChangeFeed = (*Service)(nil)

type changeSubscription struct {
	group    string
	callback func(fsnotify.Event) error
	cancel   func()
}

type OpenDB func(path, dataKey string) (*wechatdb.DB, error)

type StorageConfig interface {
	GetWorkDir() string
	GetDataDir() string
	GetDataKey() string
}

type RuntimeConfig interface {
	GetRuntimeDir() string
	GetHTTPAddr() string
	GetMessageHook() *conf.MessageHook
	// GetAccount 当前激活账号（含 local hex suffix）；server 模式返回空串。
	GetAccount() string
}

type Config interface {
	StorageConfig
	RuntimeConfig
}

func NewService(conf Config, openDB OpenDB, openHook HookFactory) *Service {
	return &Service{
		conf:          conf,
		openDB:        openDB,
		openHook:      openHook,
		subscriptions: make(map[uint64]*changeSubscription),
	}
}

func (s *Service) Start() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.db != nil {
		return nil
	}
	s.SetOpening()

	// All runtime database access goes through the built-in direct VFS and
	// points at WeChat's original db_storage. WorkDir is no longer a query
	// source and is kept only for migration of older user configuration.
	dbPath := s.conf.GetDataDir()
	if s.openDB == nil {
		return fmt.Errorf("database opener is not configured")
	}
	db, err := s.openDB(dbPath, s.conf.GetDataKey())
	if err != nil {
		s.SetError(err.Error())
		return err
	}
	s.db = db
	if db != nil {
		if err := s.bindSubscriptionsLocked(); err != nil {
			_ = db.Close()
			s.db = nil
			return err
		}
		changeFeed := newMessageChangeCoordinator(wechatMessageChangeDatabase{db: db})
		if err := changeFeed.Start(); err != nil {
			changeFeed.Stop()
			log.Warn().Err(err).Msg("shared message change coordinator unavailable")
		} else {
			s.changeMu.Lock()
			s.changeFeed = changeFeed
			s.changeMu.Unlock()
		}
	}
	if err := s.initMessageHook(); err != nil {
		s.changeMu.Lock()
		if changeFeed := s.changeFeed; changeFeed != nil {
			changeFeed.Stop()
			s.changeFeed = nil
		}
		s.changeMu.Unlock()
		s.detachSubscriptionsLocked()
		_ = db.Close()
		s.db = nil
		s.SetError(err.Error())
		return err
	}
	s.SetReady()
	s.wakeSubscriptionsLocked()
	return nil
}

func (s *Service) Stop() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	return s.stopLocked()
}

func (s *Service) stopLocked() error {
	var errs []error
	s.detachSubscriptionsLocked()
	if s.hookCancel != nil {
		s.hookCancel()
		s.hookCancel = nil
	}
	if s.hookSvc != nil {
		if err := s.hookSvc.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	s.hookSvc = nil
	s.changeMu.Lock()
	if changeFeed := s.changeFeed; changeFeed != nil {
		changeFeed.Stop()
		s.changeFeed = nil
	}
	s.changeMu.Unlock()
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	s.SetInit()
	s.db = nil
	return stderrors.Join(errs...)
}

func (s *Service) SetInit() {
	s.State = StateInit
}

func (s *Service) SetOpening() {
	s.State = StateOpening
}

func (s *Service) SetReady() {
	s.State = StateReady
}

func (s *Service) SetError(msg string) {
	s.State = StateError
	s.StateMsg = msg
}

func (s *Service) GetDB() *wechatdb.DB {
	return s.db
}

func (s *Service) Status() (ports.DatabaseState, string) {
	if s == nil {
		return StateInit, ""
	}
	return s.State, s.StateMsg
}

func (s *Service) Ready() bool {
	return s != nil && s.State == StateReady && s.db != nil
}

func (s *Service) GetMessages(start, end time.Time, talker string, sender string, keyword string, limit, offset int) ([]*model.Message, error) {
	return s.db.GetMessages(start, end, talker, sender, keyword, limit, offset)
}

func (s *Service) GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error) {
	return s.db.GetMessagesAfter(talker, cursor, limit)
}

// SubscribeMessageChanges joins the shared single-read message stream used by
// OCR and push delivery. Consumers keep their own durable business cursors.
func (s *Service) SubscribeMessageChanges(
	name string,
	since time.Time,
	handler ports.MessageChangeHandler,
) (func(), error) {
	if s == nil {
		return nil, fmt.Errorf("message change coordinator is not running")
	}
	s.changeMu.RLock()
	defer s.changeMu.RUnlock()
	if s.changeFeed == nil {
		return nil, fmt.Errorf("message change coordinator is not running")
	}
	return s.changeFeed.Subscribe(name, since, handler)
}

func (s *Service) MessageChangeStats() ports.MessageChangeStats {
	if s == nil {
		return ports.MessageChangeStats{}
	}
	s.changeMu.RLock()
	defer s.changeMu.RUnlock()
	if s.changeFeed == nil {
		return ports.MessageChangeStats{}
	}
	return s.changeFeed.Stats()
}

func (s *Service) SearchMessages(
	ctx context.Context,
	startTime, endTime time.Time,
	talkers []string,
	keyword string,
	messageType int64,
	limit, offset int,
) ([]*model.Message, int, string, error) {
	if s == nil || s.db == nil {
		return nil, 0, "", fmt.Errorf("database not ready")
	}
	return s.db.SearchMessages(ctx, startTime, endTime, talkers, keyword, messageType, limit, offset)
}

func (s *Service) SearchMessagesAdvanced(
	ctx context.Context,
	request model.MessageSearchRequest,
) (model.MessageSearchResult, error) {
	if s == nil || s.db == nil {
		return model.MessageSearchResult{}, fmt.Errorf("database not ready")
	}
	return s.db.SearchMessagesAdvanced(ctx, request)
}

func (s *Service) AnalyzeMessages(
	ctx context.Context,
	request model.MessageAnalyticsRequest,
) (model.MessageAnalyticsResult, error) {
	if s == nil || s.db == nil {
		return model.MessageAnalyticsResult{}, fmt.Errorf("database not ready")
	}
	return s.db.AnalyzeMessages(ctx, request)
}

func (s *Service) GetMessage(talker string, seq int64) (*model.Message, error) {
	return s.db.GetMessage(talker, seq)
}

func (s *Service) GetContacts(key string, limit, offset int) (*ports.Contacts, error) {
	result, err := s.db.GetContacts(key, limit, offset)
	if err != nil {
		return nil, err
	}
	return &ports.Contacts{Items: result.Items}, nil
}

func (s *Service) GetContact(key string) (*model.Contact, error) {
	return s.db.GetContact(key)
}

func (s *Service) GetChatRooms(key string, limit, offset int) (*ports.ChatRooms, error) {
	result, err := s.db.GetChatRooms(key, limit, offset)
	if err != nil {
		return nil, err
	}
	return &ports.ChatRooms{Items: result.Items}, nil
}

func (s *Service) GetChatRoom(key string) (*model.ChatRoom, error) {
	return s.db.GetChatRoom(key)
}

// GetSession retrieves session information
func (s *Service) GetSessions(key string, limit, offset int) (*ports.Sessions, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database not ready")
	}
	result, err := s.db.GetSessions(key, limit, offset)
	if err != nil {
		return nil, err
	}
	return &ports.Sessions{Items: result.Items}, nil
}

func (s *Service) GetMedia(_type string, key string) (*model.Media, error) {
	return s.db.GetMedia(_type, key)
}

func (s *Service) GetMediaByName(_type string, name string, size int64) (*model.Media, error) {
	return s.db.GetMediaByName(_type, name, size)
}

func (s *Service) SetCallback(group string, callback func(event fsnotify.Event) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database not ready")
	}
	return s.db.SetCallback(group, callback)
}

// SubscribeCallback registers a logical database-change subscription. It may
// be called before Start; the listener is bound when the database becomes
// ready, detached on Stop, and rebound on the next Start. The returned function
// permanently removes the logical subscription.
func (s *Service) SubscribeCallback(group string, callback func(event fsnotify.Event) error) (func(), error) {
	if s == nil {
		return nil, fmt.Errorf("database service is nil")
	}
	if callback == nil {
		return func() {}, nil
	}

	s.lifecycle.Lock()
	s.subscriptionMu.Lock()
	s.nextSubscriptionID++
	id := s.nextSubscriptionID
	subscription := &changeSubscription{group: group, callback: callback}
	s.subscriptions[id] = subscription
	if s.db != nil {
		cancel, err := s.db.SubscribeCallback(group, callback)
		if err != nil {
			delete(s.subscriptions, id)
			s.subscriptionMu.Unlock()
			s.lifecycle.Unlock()
			return nil, err
		}
		subscription.cancel = cancel
	}
	databaseReady := s.Ready()
	s.subscriptionMu.Unlock()
	s.lifecycle.Unlock()

	if databaseReady {
		_ = callback(fsnotify.Event{Op: fsnotify.Write})
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.lifecycle.Lock()
			s.subscriptionMu.Lock()
			if active := s.subscriptions[id]; active != nil {
				if active.cancel != nil {
					active.cancel()
				}
				delete(s.subscriptions, id)
			}
			s.subscriptionMu.Unlock()
			s.lifecycle.Unlock()
		})
	}, nil
}

func (s *Service) bindSubscriptionsLocked() error {
	s.subscriptionMu.Lock()
	defer s.subscriptionMu.Unlock()
	for _, subscription := range s.subscriptions {
		if subscription == nil || subscription.cancel != nil {
			continue
		}
		cancel, err := s.db.SubscribeCallback(subscription.group, subscription.callback)
		if err != nil {
			for _, bound := range s.subscriptions {
				if bound != nil && bound.cancel != nil {
					bound.cancel()
					bound.cancel = nil
				}
			}
			return fmt.Errorf("subscribe database group %s: %w", subscription.group, err)
		}
		subscription.cancel = cancel
	}
	return nil
}

func (s *Service) detachSubscriptionsLocked() {
	s.subscriptionMu.Lock()
	defer s.subscriptionMu.Unlock()
	for _, subscription := range s.subscriptions {
		if subscription != nil && subscription.cancel != nil {
			subscription.cancel()
			subscription.cancel = nil
		}
	}
}

func (s *Service) wakeSubscriptionsLocked() {
	s.subscriptionMu.Lock()
	callbacks := make([]func(fsnotify.Event) error, 0, len(s.subscriptions))
	for _, subscription := range s.subscriptions {
		if subscription != nil && subscription.callback != nil {
			callbacks = append(callbacks, subscription.callback)
		}
	}
	s.subscriptionMu.Unlock()
	for _, callback := range callbacks {
		if err := callback(fsnotify.Event{Op: fsnotify.Write}); err != nil {
			log.Debug().Err(err).Msg("wake database change subscription")
		}
	}
}

func (s *Service) GetDecryptedDBs() (map[string][]string, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.GetDBs()
}

func (s *Service) GetTables(group, file string) ([]string, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.GetTables(group, file)
}

func (s *Service) GetTableData(group, file, table string, limit, offset int, keyword string) ([]map[string]interface{}, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.GetTableData(group, file, table, limit, offset, keyword)
}

func (s *Service) StreamTableData(
	ctx context.Context,
	group string,
	file string,
	table string,
	keyword string,
	visit func(columns []string, values []interface{}) error,
) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	return s.db.StreamTableData(ctx, group, file, table, keyword, visit)
}

func (s *Service) ExecuteSQL(group, file, query string) ([]map[string]interface{}, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.ExecuteSQL(group, file, query)
}

func (s *Service) StreamSQL(
	ctx context.Context,
	group string,
	file string,
	query string,
	visit func(columns []string, values []interface{}) error,
) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	return s.db.StreamSQL(ctx, group, file, query, visit)
}

func (s *Service) SearchAll(keyword string, limit int, deep bool) ([]map[string]interface{}, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.SearchAll(keyword, limit, deep)
}

func (s *Service) SearchAllDetailed(
	ctx context.Context,
	request model.DatabaseSearchRequest,
) (model.DatabaseSearchResult, error) {
	if s == nil || s.db == nil {
		return model.DatabaseSearchResult{}, fmt.Errorf("database not ready")
	}
	return s.db.SearchAllDetailed(ctx, request)
}

func (s *Service) ClearDecryptedCache() (int64, int64, error) {
	return wcdbapi.ClearDecryptedCacheDirectory(util.DefaultCacheDir("wcdb"))
}

func (s *Service) initMessageHook() error {
	if s.db == nil || s.openHook == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.hookCancel = cancel
	hookSvc, err := s.openHook(s.conf, s)
	if err != nil {
		cancel()
		s.hookCancel = nil
		return err
	}
	s.hookSvc = hookSvc
	go s.hookSvc.Run(ctx)
	log.Info().Msg("message hook service started")
	return nil
}

// Close closes the database connection
func (s *Service) Close() {
	_ = s.Stop()
}

func (s *Service) WakeMessageHook() {
	if s != nil && s.hookSvc != nil {
		s.hookSvc.Wake()
	}
}

func (s *Service) GetMessageHookEvents(limit int) ([]ports.HookEvent, error) {
	if s == nil || s.hookSvc == nil {
		return []ports.HookEvent{}, nil
	}
	return s.hookSvc.RecentEvents(limit)
}

func (s *Service) ClearMessageHookEvents() (int, error) {
	if s == nil || s.hookSvc == nil {
		return 0, nil
	}
	return s.hookSvc.ClearEvents()
}

func (s *Service) CancelMessageHookEvent(eventID, target string) (int64, error) {
	if s == nil || s.hookSvc == nil {
		return 0, nil
	}
	return s.hookSvc.CancelEvent(eventID, target)
}

func (s *Service) RetryMessageHookEvent(eventID, target string) (int64, error) {
	if s == nil || s.hookSvc == nil {
		return 0, nil
	}
	return s.hookSvc.RetryEvent(eventID, target)
}

func (s *Service) DeleteMessageHookEvent(eventID, target string) (int64, error) {
	if s == nil || s.hookSvc == nil {
		return 0, nil
	}
	deleter, ok := s.hookSvc.(interface {
		DeleteEvent(eventID, target string) (int64, error)
	})
	if !ok {
		return 0, fmt.Errorf("message hook runtime does not support deleting events")
	}
	return deleter.DeleteEvent(eventID, target)
}

func (s *Service) RetryMessageHookEvents(eventIDs []string, target string) (int64, error) {
	if s == nil || s.hookSvc == nil {
		return 0, nil
	}
	retrier, ok := s.hookSvc.(interface {
		RetryEvents(eventIDs []string, target string) (int64, error)
	})
	if !ok {
		return 0, fmt.Errorf("message hook runtime does not support batch retry")
	}
	return retrier.RetryEvents(eventIDs, target)
}

func (s *Service) DeleteMessageHookEvents(eventIDs []string, target string) (int64, error) {
	if s == nil || s.hookSvc == nil {
		return 0, nil
	}
	deleter, ok := s.hookSvc.(interface {
		DeleteEvents(eventIDs []string, target string) (int64, error)
	})
	if !ok {
		return 0, fmt.Errorf("message hook runtime does not support batch delete")
	}
	return deleter.DeleteEvents(eventIDs, target)
}

func (s *Service) NotifyMessageHookOCR(message *model.Message, ocrText string) (bool, error) {
	if s == nil || s.hookSvc == nil {
		return false, nil
	}
	notifier, ok := s.hookSvc.(interface {
		NotifyOCRResult(message *model.Message, ocrText string) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return notifier.NotifyOCRResult(message, ocrText)
}

func (s *Service) GetMessageHookStats() (ports.HookRuntimeStats, error) {
	if s == nil || s.hookSvc == nil {
		return ports.HookRuntimeStats{}, nil
	}
	return s.hookSvc.Stats()
}

func (s *Service) MessageHookStorePath() string {
	if s == nil || s.hookSvc == nil {
		return ""
	}
	return s.hookSvc.StorePath()
}

// GetSNSTimeline 获取朋友圈时间线数据
func (s *Service) GetSNSTimeline(username string, limit, offset int) ([]map[string]interface{}, error) {
	if s.db == nil {
		return nil, nil
	}
	return s.db.GetSNSTimeline(username, limit, offset)
}

// GetSNSCount 获取朋友圈数量统计
func (s *Service) GetSNSCount(username string) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	return s.db.GetSNSCount(username)
}
