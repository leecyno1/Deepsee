package messagehook

import (
	"context"
	"crypto/sha256"
	stderrors "errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/pkg/util"
)

// chatlog 的 data_dir 命名规则：wxid_<id>_<hex-local-suffix>；strip 后即真正的
// 微信 wxid（例：wxid_example_1a2b → wxid_example）。
var chatlogLocalSuffixRe = regexp.MustCompile(`_[0-9a-f]{1,8}$`)

func stripChatlogLocalSuffix(account string) string {
	if account == "" {
		return ""
	}
	return chatlogLocalSuffixRe.ReplaceAllString(account, "")
}

const (
	changeDebounce       = 250 * time.Millisecond
	postRetryBaseDelay   = 5 * time.Second
	messagePageSize      = 500
	deliveryWorkers      = 3
	maxContextScan       = 2000
	voiceResolveRetries  = 20
	voiceResolveInterval = 500 * time.Millisecond
	maxPostRetryDelay    = time.Minute
	storePruneInterval   = time.Hour
)

type Config interface {
	GetMessageHook() *conf.MessageHook
	GetDataDir() string
	GetWorkDir() string
	GetRuntimeDir() string
	GetHTTPAddr() string
	// GetAccount 返回当前 chatlog 监听的 raw account ID（含 local hex suffix，如
	// wxid_example_1a2b）。messagehook strip 后填到 Event.OwnerWxid，让 webhook
	// 接收方能判断消息属于哪个微信账号，避免切号后数据错乱。
	GetAccount() string
}

type MessageDatabase interface {
	GetSessions(key string, limit, offset int) (*ports.Sessions, error)
	GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error)
	GetMessages(start, end time.Time, talker, sender, keyword string, limit, offset int) ([]*model.Message, error)
	GetMessage(talker string, seq int64) (*model.Message, error)
	GetMedia(mediaType, key string) (*model.Media, error)
	SetCallback(group string, callback func(event fsnotify.Event) error) error
}

type ContextMessage = ports.HookContextMessage
type Event = ports.HookEvent
type DeliveryResult = ports.HookDeliveryResult

type Service struct {
	conf       Config
	db         MessageDatabase
	httpClient *http.Client
	store      *outboxStore

	scanWake     chan struct{}
	deliveryWake chan struct{}
	runDone      chan struct{}
	changeFeed   ports.MessageChangeFeed

	activeDeliveryMu sync.Mutex
	activeDeliveries map[string]*activeDelivery

	statsMu          sync.RWMutex
	scanMu           sync.Mutex
	lastScanAt       string
	lastScanDuration time.Duration
	lastScanError    string
	scannedSessions  int
	scannedMessages  int
	matchedMessages  int
}

type RuntimeStats = ports.HookRuntimeStats

func New(conf Config, db MessageDatabase) (*Service, error) {
	base := strings.TrimSpace(conf.GetWorkDir())
	if base == "" {
		base = util.WorkDirAt(conf.GetRuntimeDir(), "server")
	}
	store, err := openOutboxStore(filepath.Join(base, "runtime", "hook.db"))
	if err != nil {
		return nil, err
	}
	service := &Service{
		conf:             conf,
		db:               db,
		httpClient:       &http.Client{Timeout: 10 * time.Second},
		store:            store,
		scanWake:         make(chan struct{}, 1),
		deliveryWake:     make(chan struct{}, 1),
		runDone:          make(chan struct{}),
		activeDeliveries: make(map[string]*activeDelivery),
	}
	if feed, ok := db.(ports.MessageChangeFeed); ok {
		service.changeFeed = feed
	} else {
		for _, group := range []string{"message", "session"} {
			if err := db.SetCallback(group, func(fsnotify.Event) error {
				service.Wake()
				return nil
			}); err != nil {
				_ = store.Close()
				return nil, fmt.Errorf("watch message hook database group %s: %w", group, err)
			}
		}
	}
	return service, nil
}

func (s *Service) Run(ctx context.Context) {
	defer close(s.runDone)
	if s.changeFeed != nil {
		cancel, err := s.changeFeed.SubscribeMessageChanges("message_hook", time.Now(), s.consumeMessageChangeBatch)
		if err != nil {
			log.Warn().Err(err).Msg("subscribe shared message changes for hook")
			for _, group := range []string{"message", "session"} {
				if watchErr := s.db.SetCallback(group, func(fsnotify.Event) error {
					s.Wake()
					return nil
				}); watchErr != nil {
					log.Warn().Err(watchErr).Str("group", group).Msg("install hook database watcher fallback")
				}
			}
		} else {
			defer cancel()
		}
	}
	var workers sync.WaitGroup
	for i := 0; i < deliveryWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.runDeliveryWorker(ctx)
		}()
	}
	defer workers.Wait()
	s.runScan(ctx)
	_ = s.store.Prune(time.Now())
	pruneTicker := time.NewTicker(storePruneInterval)
	defer pruneTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pruneTicker.C:
			_ = s.store.Prune(time.Now())
		case <-s.scanWake:
			// WCDB commonly emits several WAL/SHM notifications for one commit.
			// Coalesce that burst into one incremental query without introducing a
			// periodic database scan.
			timer := time.NewTimer(changeDebounce)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			for {
				select {
				case <-s.scanWake:
				default:
					goto scan
				}
			}
		scan:
			s.runScan(ctx)
		}
	}
}

func (s *Service) Wake() {
	select {
	case s.scanWake <- struct{}{}:
	default:
	}
}

func (s *Service) runScan(ctx context.Context) {
	started := time.Now()
	sessions, messages, matched, err := s.scanOnceContext(ctx)
	s.statsMu.Lock()
	s.lastScanAt = time.Now().Format(time.RFC3339)
	s.lastScanDuration = time.Since(started)
	s.scannedSessions = sessions
	s.scannedMessages = messages
	s.matchedMessages = matched
	s.lastScanError = ""
	if err != nil {
		s.lastScanError = err.Error()
	}
	s.statsMu.Unlock()
	if err != nil && !stderrors.Is(err, context.Canceled) {
		log.Warn().Err(err).Msg("message hook scan incomplete")
	}
}

func (s *Service) scanOnce() (int, int, int, error) {
	return s.scanOnceContext(context.Background())
}

func (s *Service) scanOnceContext(ctx context.Context) (int, int, int, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, 0, 0, err
	}
	account := stripChatlogLocalSuffix(s.conf.GetAccount())
	if account == "" {
		account = "server"
	}
	cfg := s.conf.GetMessageHook()
	if !hookConfigActive(cfg) {
		state, exists, err := s.store.Activation(account)
		if err != nil {
			return 0, 0, 0, err
		}
		if !exists || state.Enabled {
			if err := s.store.SetInactive(account, time.Now()); err != nil {
				return 0, 0, 0, err
			}
		}
		return 0, 0, 0, nil
	}
	keywords := parseKeywords(cfg.Keywords)
	forwardContacts := parseTargetList(cfg.ForwardContacts)
	forwardChatRooms := parseTargetList(cfg.ForwardChatRooms)
	ruleFingerprint := pushRuleFingerprint(cfg)
	whitelistOnly := len(keywords) == 0 && !cfg.ForwardAll
	if err := ctx.Err(); err != nil {
		return 0, 0, 0, err
	}
	sessions, err := s.db.GetSessions("", 0, 0)
	if err != nil {
		return 0, 0, 0, err
	}
	var sessionItems []*model.Session
	if sessions != nil {
		sessionItems = sessions.Items
	}

	activation, exists, err := s.store.Activation(account)
	if err != nil {
		return len(sessionItems), 0, 0, err
	}
	if !exists || !activation.Enabled || activation.RuleFingerprint != ruleFingerprint {
		enabledAt := time.Now()
		if cfg.EffectiveAt > 0 {
			enabledAt = time.Unix(cfg.EffectiveAt, 0)
		}
		cursors, err := s.snapshotSessionCursors(ctx, sessionItems, enabledAt)
		if err != nil {
			return len(sessionItems), 0, 0, err
		}
		if err := s.store.Activate(account, enabledAt, cursors, ruleFingerprint); err != nil {
			return len(sessionItems), 0, 0, err
		}
		// The activation scan only establishes the high-water mark. Delivery
		// starts with the next database change, so existing rows are never
		// interpreted as newly received messages.
		return len(sessionItems), 0, 0, nil
	}
	targets := deliveryTargets(cfg)
	var scanErrors []error
	scannedMessages := 0
	matchedMessages := 0
	for _, sess := range sessionItems {
		if err := ctx.Err(); err != nil {
			return len(sessionItems), scannedMessages, matchedMessages, err
		}
		if sess == nil || strings.TrimSpace(sess.UserName) == "" {
			continue
		}
		if whitelistOnly && !sessionInForwardWhitelist(sess.UserName, sess.NickName, forwardContacts, forwardChatRooms) {
			continue
		}
		messages, matched, err := s.scanTalkerContext(ctx, account, sess.UserName, sess.NTime, activation, keywords, forwardContacts, forwardChatRooms, cfg, targets)
		scannedMessages += messages
		matchedMessages += matched
		if err != nil {
			scanErrors = append(scanErrors, fmt.Errorf("%s: %w", sess.UserName, err))
		}
	}
	return len(sessionItems), scannedMessages, matchedMessages, stderrors.Join(scanErrors...)
}

func hookConfigActive(cfg *conf.MessageHook) bool {
	if cfg == nil {
		return false
	}
	return len(parseKeywords(cfg.Keywords)) > 0 ||
		cfg.ForwardAll ||
		len(parseTargetList(cfg.ForwardContacts)) > 0 ||
		len(parseTargetList(cfg.ForwardChatRooms)) > 0
}

// pushRuleFingerprint identifies only fields that decide whether a message
// matches. Delivery destinations and context sizes may change without creating
// a new matching epoch. Target lists are canonicalized so formatting-only edits
// do not discard messages waiting after the current cursor.
func pushRuleFingerprint(cfg *conf.MessageHook) string {
	if cfg == nil {
		return ""
	}
	canonicalTargets := func(raw string) string {
		items := parseTargetList(raw)
		values := make([]string, 0, len(items))
		for value := range items {
			values = append(values, value)
		}
		sort.Strings(values)
		return strings.Join(values, "\x1f")
	}
	payload := strings.Join([]string{
		strings.Join(parseKeywords(cfg.Keywords), "\x1f"),
		conf.CanonicalHookKeywordMode(cfg.KeywordMode),
		canonicalTargets(cfg.ForwardContacts),
		canonicalTargets(cfg.ForwardChatRooms),
		fmt.Sprintf("%t", cfg.ForwardAll),
	}, "\x1e")
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("v2:%x", digest[:])
}

// NotifyOCRResult evaluates an already-recognized image against keyword rules.
// It uses the same durable event identity as the ordinary scanner, so an image
// already forwarded by a contact/all rule is not delivered twice.
func (s *Service) NotifyOCRResult(message *model.Message, ocrText string) (bool, error) {
	if s == nil || s.store == nil || message == nil || message.IsSelf ||
		message.Type != model.MessageTypeImage {
		return false, nil
	}
	cfg := s.conf.GetMessageHook()
	if cfg == nil || !conf.HookKeywordModeIncludesImageOCR(cfg.KeywordMode) {
		return false, nil
	}
	account := stripChatlogLocalSuffix(s.conf.GetAccount())
	if account == "" {
		account = "server"
	}
	activation, exists, err := s.store.Activation(account)
	if err != nil {
		return false, err
	}
	// OCR backfill and delayed OCR completion must follow the same rule epoch as
	// ordinary message scanning. Old images are never turned into new pushes.
	if !exists || !activation.Enabled ||
		activation.RuleFingerprint != pushRuleFingerprint(cfg) ||
		message.Time.IsZero() || message.Time.Unix() <= activation.EnabledAt {
		s.Wake()
		return false, nil
	}
	keywords := parseKeywords(cfg.Keywords)
	if len(keywords) == 0 {
		return false, nil
	}
	ocrText = strings.TrimSpace(ocrText)
	keyword := matchKeyword(ocrText, keywords)
	if keyword == "" {
		return false, nil
	}
	rules := []RuleMatch{{
		RuleType:  "keyword",
		RuleLabel: keyword,
		Keyword:   keyword,
	}}
	content := "图片 OCR：" + ocrText
	event := s.buildEventForRules(message, rules, content, cfg)
	if err := s.store.EnqueueEvent(event, deliveryTargets(cfg)); err != nil {
		return false, err
	}
	s.wakeDeliveries()
	return true, nil
}

func (s *Service) snapshotSessionCursors(ctx context.Context, sessions []*model.Session, enabledAt time.Time) (map[string]model.MessageCursor, error) {
	cursors := make(map[string]model.MessageCursor, len(sessions))
	fallback := model.MessageCursor{Timestamp: enabledAt.Unix(), LocalID: math.MaxInt64}
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if session == nil || strings.TrimSpace(session.UserName) == "" {
			continue
		}
		messages, err := s.db.GetMessages(time.Time{}, enabledAt, session.UserName, "", "", 1, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: capture activation cursor: %w", session.UserName, err)
		}
		cursor := fallback
		for _, message := range messages {
			if message == nil {
				continue
			}
			candidate := model.MessageCursor{Timestamp: message.Time.Unix(), LocalID: message.DBLocalID}
			if cursor == fallback || cursor.Before(candidate) {
				cursor = candidate
			}
		}
		if len(messages) == 0 && !session.NTime.IsZero() && !session.NTime.After(enabledAt) {
			cursor = model.MessageCursor{Timestamp: session.NTime.Unix(), LocalID: math.MaxInt64}
		}
		cursors[session.UserName] = cursor
	}
	return cursors, nil
}

func sessionInForwardWhitelist(talker, talkerName string, contacts, chatrooms map[string]struct{}) bool {
	return targetListContains(contacts, talker, talkerName) || targetListContains(chatrooms, talker, talkerName)
}

func (s *Service) scanTalker(account, talker string, latestSessionTime time.Time, keywords []string, forwardContacts, forwardChatRooms map[string]struct{}, cfg *conf.MessageHook, targets []deliveryTarget) (int, int, error) {
	activation, _, err := s.store.Activation(account)
	if err != nil {
		return 0, 0, err
	}
	return s.scanTalkerContext(context.Background(), account, talker, latestSessionTime, activation, keywords, forwardContacts, forwardChatRooms, cfg, targets)
}

func (s *Service) scanTalkerContext(ctx context.Context, account, talker string, latestSessionTime time.Time, activation hookActivation, keywords []string, forwardContacts, forwardChatRooms map[string]struct{}, cfg *conf.MessageHook, targets []deliveryTarget) (int, int, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	cursor, exists, err := s.store.Cursor(account, talker)
	if err != nil {
		return 0, 0, err
	}
	if !exists {
		cursor = model.MessageCursor{Timestamp: activation.EnabledAt, LocalID: math.MaxInt64}
		if err := s.store.Commit(account, talker, cursor, nil, nil); err != nil {
			return 0, 0, err
		}
	}
	// SessionTable already exposes the newest timestamp. Once a conversation is
	// strictly older than its durable cursor, avoid opening every message shard
	// for that inactive conversation on each change event. Equality still
	// queries because multiple rows can share the same second.
	if !latestSessionTime.IsZero() && latestSessionTime.Unix() < cursor.Timestamp {
		return 0, 0, nil
	}
	scanned := 0
	matched := 0
	for {
		if err := ctx.Err(); err != nil {
			return scanned, matched, err
		}
		msgs, err := s.db.GetMessagesAfter(talker, cursor, messagePageSize)
		if err != nil {
			return scanned, matched, err
		}
		if len(msgs) == 0 {
			break
		}
		pageEvents := make([]Event, 0)
		pageCursor := cursor
		for _, message := range msgs {
			if err := ctx.Err(); err != nil {
				return scanned, matched, err
			}
			if message == nil || !cursor.BeforeMessage(message) {
				continue
			}
			nextCursor := model.MessageCursor{Timestamp: message.Time.Unix(), LocalID: message.DBLocalID}
			scanned++
			var event *Event
			if !message.IsSelf {
				content := strings.TrimSpace(message.PlainTextContent())
				if content == "" {
					content = strings.TrimSpace(message.Content)
				}
				rules := s.matchRules(message, content, keywords, cfg.KeywordMode, forwardContacts, forwardChatRooms, cfg.ForwardAll)
				if len(rules) > 0 {
					built := s.buildEventForRules(message, rules, content, cfg)
					event = &built
					matched++
				}
			}
			pageCursor = nextCursor
			if event != nil {
				pageEvents = append(pageEvents, *event)
			}
		}
		if pageCursor != cursor {
			if err := s.store.CommitEvents(account, talker, pageCursor, pageEvents, targets); err != nil {
				return scanned, matched, err
			}
			cursor = pageCursor
			if len(pageEvents) > 0 {
				s.wakeDeliveries()
			}
		}
		if len(msgs) < messagePageSize {
			break
		}
	}
	return scanned, matched, nil
}
