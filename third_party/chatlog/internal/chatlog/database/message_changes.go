package database

import (
	"context"
	stderrors "errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/internal/wechatdb"
)

const (
	messageChangePageSize       = 500
	messageChangeDebounce       = 40 * time.Millisecond
	messageChangeReplayBatches  = 128
	messageChangeReplayMessages = 10_000
	messageChangeReplayAge      = 5 * time.Minute
	messageChangeWarningWindow  = 30 * time.Second
)

type messageChangeCoordinator struct {
	db        messageChangeDatabase
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	wake      chan struct{}
	startedAt time.Time

	mu             sync.Mutex
	cursors        map[string]model.MessageCursor
	subscriptions  map[uint64]*messageChangeSubscription
	nextSubID      uint64
	nextSequence   uint64
	replay         []ports.MessageChangeBatch
	replayMessages int
	watchCancels   []func()
	eventSequence  uint64
	pendingFiles   map[string]uint64
	pendingGeneric uint64
	stats          ports.MessageChangeStats

	lastScanWarningKey  string
	lastScanWarningAt   time.Time
	suppressedScanWarns uint64
}

type messageChangeDatabase interface {
	GetSessions(key string, limit, offset int) (*ports.Sessions, error)
	GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error)
	SubscribeCallback(group string, callback func(fsnotify.Event) error) (func(), error)
}

type targetedMessageChangeDatabase interface {
	ResolveChangedMessageTalkers(changedFiles []string, candidates map[string]model.MessageCursor) ([]string, error)
	GetMessagesAfterFiles(talker string, cursor model.MessageCursor, limit int, changedFiles []string) ([]*model.Message, error)
}

type messageChangeSnapshot struct {
	files   map[string]uint64
	generic uint64
}

func (s messageChangeSnapshot) paths() []string {
	paths := make([]string, 0, len(s.files))
	for path := range s.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

type wechatMessageChangeDatabase struct{ db *wechatdb.DB }

func (w wechatMessageChangeDatabase) GetSessions(key string, limit, offset int) (*ports.Sessions, error) {
	result, err := w.db.GetSessions(key, limit, offset)
	if err != nil {
		return nil, err
	}
	return &ports.Sessions{Items: result.Items}, nil
}

func (w wechatMessageChangeDatabase) GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error) {
	return w.db.GetMessagesAfter(talker, cursor, limit)
}

func (w wechatMessageChangeDatabase) ResolveChangedMessageTalkers(
	changedFiles []string,
	candidates map[string]model.MessageCursor,
) ([]string, error) {
	return w.db.ResolveChangedMessageTalkers(changedFiles, candidates)
}

func (w wechatMessageChangeDatabase) GetMessagesAfterFiles(
	talker string,
	cursor model.MessageCursor,
	limit int,
	changedFiles []string,
) ([]*model.Message, error) {
	return w.db.GetMessagesAfterFiles(talker, cursor, limit, changedFiles)
}

func (w wechatMessageChangeDatabase) SubscribeCallback(group string, callback func(fsnotify.Event) error) (func(), error) {
	return w.db.SubscribeCallback(group, callback)
}

type messageChangeSubscription struct {
	id      uint64
	name    string
	handler ports.MessageChangeHandler
	ctx     context.Context
	cancel  context.CancelFunc
	queue   chan ports.MessageChangeBatch
	done    chan struct{}
}

func newMessageChangeCoordinator(db messageChangeDatabase) *messageChangeCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	return &messageChangeCoordinator{
		db:            db,
		ctx:           ctx,
		cancel:        cancel,
		wake:          make(chan struct{}, 1),
		startedAt:     now,
		cursors:       make(map[string]model.MessageCursor),
		subscriptions: make(map[uint64]*messageChangeSubscription),
		pendingFiles:  make(map[string]uint64),
		stats: ports.MessageChangeStats{
			Running: true,
			Mode:    "shared_database_event_stream",
		},
	}
}

func (c *messageChangeCoordinator) Start() error {
	if c == nil || c.db == nil {
		return fmt.Errorf("message change coordinator database is not ready")
	}
	for _, group := range []string{"message", "session"} {
		group := group
		cancel, err := c.db.SubscribeCallback(group, func(event fsnotify.Event) error {
			if event.Op.Has(fsnotify.Create) || event.Op.Has(fsnotify.Write) ||
				event.Op.Has(fsnotify.Rename) || event.Op.Has(fsnotify.Remove) {
				c.mu.Lock()
				c.eventSequence++
				if group == "message" && strings.TrimSpace(event.Name) != "" {
					c.pendingFiles[event.Name] = c.eventSequence
				} else {
					c.pendingGeneric = c.eventSequence
				}
				c.stats.FilesystemEvents++
				c.stats.LastEventAt = time.Now().Format(time.RFC3339Nano)
				c.mu.Unlock()
				c.Wake()
			}
			return nil
		})
		if err != nil {
			for _, stop := range c.watchCancels {
				stop()
			}
			c.watchCancels = nil
			return fmt.Errorf("watch message change group %s: %w", group, err)
		}
		c.watchCancels = append(c.watchCancels, cancel)
	}
	c.wg.Add(1)
	go c.run()
	return nil
}

func (c *messageChangeCoordinator) Stop() {
	if c == nil {
		return
	}
	for _, cancel := range c.watchCancels {
		if cancel != nil {
			cancel()
		}
	}
	c.watchCancels = nil
	c.cancel()
	c.mu.Lock()
	for _, subscription := range c.subscriptions {
		subscription.cancel()
	}
	c.subscriptions = make(map[uint64]*messageChangeSubscription)
	c.stats.Running = false
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *messageChangeCoordinator) Wake() {
	if c == nil {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *messageChangeCoordinator) run() {
	defer c.wg.Done()
	retryDelay := time.Duration(0)
	var retryTimer *time.Timer
	var retryC <-chan time.Time
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
			if retryTimer != nil && !retryTimer.Stop() {
				select {
				case <-retryTimer.C:
				default:
				}
			}
			retryC = nil
		case <-retryC:
			retryC = nil
		}
		if !c.waitForDebounce() {
			return
		}
		change := c.snapshotPendingChanges()
		if len(change.files) == 0 && change.generic == 0 {
			continue
		}
		started := time.Now()
		err := c.scan(change)
		c.mu.Lock()
		c.stats.Scans++
		c.stats.LastScanAt = time.Now().Format(time.RFC3339Nano)
		c.stats.LastScanDurationMS = time.Since(started).Milliseconds()
		if err != nil {
			c.stats.LastError = err.Error()
		} else {
			c.stats.LastError = ""
		}
		c.mu.Unlock()
		if err != nil {
			if emit, suppressed := c.shouldLogScanWarning(err, time.Now()); emit {
				event := log.Warn().Err(err)
				if suppressed > 0 {
					event = event.Uint64("suppressed_repeats", suppressed)
				}
				event.Msg("shared message change scan incomplete")
			}
			if retryDelay == 0 {
				retryDelay = 250 * time.Millisecond
			} else {
				retryDelay *= 2
				if retryDelay > 5*time.Second {
					retryDelay = 5 * time.Second
				}
			}
			if retryTimer == nil {
				retryTimer = time.NewTimer(retryDelay)
			} else {
				retryTimer.Reset(retryDelay)
			}
			retryC = retryTimer.C
		} else {
			c.clearScanWarning()
			c.ackPendingChanges(change)
			retryDelay = 0
			retryC = nil
		}
	}
}

func (c *messageChangeCoordinator) shouldLogScanWarning(err error, now time.Time) (bool, uint64) {
	key := messageChangeWarningKey(err)
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.lastScanWarningKey && !c.lastScanWarningAt.IsZero() && now.Sub(c.lastScanWarningAt) < messageChangeWarningWindow {
		c.suppressedScanWarns++
		c.stats.SuppressedWarnings++
		return false, 0
	}
	suppressed := c.suppressedScanWarns
	c.suppressedScanWarns = 0
	c.lastScanWarningKey = key
	c.lastScanWarningAt = now
	return true, suppressed
}

func (c *messageChangeCoordinator) clearScanWarning() {
	c.mu.Lock()
	c.lastScanWarningKey = ""
	c.lastScanWarningAt = time.Time{}
	c.suppressedScanWarns = 0
	c.mu.Unlock()
}

func messageChangeWarningKey(err error) string {
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, fragment := range []string{
		"file is not a database",
		"database disk image is malformed",
		"encrypted database changed during direct query",
		"encrypted database snapshot has an invalid sqlite header",
	} {
		if strings.Contains(message, fragment) {
			return fragment
		}
	}
	return message
}

func (c *messageChangeCoordinator) snapshotPendingChanges() messageChangeSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := messageChangeSnapshot{
		files:   make(map[string]uint64, len(c.pendingFiles)),
		generic: c.pendingGeneric,
	}
	for path, sequence := range c.pendingFiles {
		snapshot.files[path] = sequence
	}
	return snapshot
}

func (c *messageChangeCoordinator) ackPendingChanges(snapshot messageChangeSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for path, sequence := range snapshot.files {
		if c.pendingFiles[path] <= sequence {
			delete(c.pendingFiles, path)
		}
	}
	if c.pendingGeneric <= snapshot.generic {
		c.pendingGeneric = 0
	}
}

func (c *messageChangeCoordinator) waitForDebounce() bool {
	timer := time.NewTimer(messageChangeDebounce)
	defer timer.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return false
		case <-timer.C:
			for {
				select {
				case <-c.wake:
				default:
					return true
				}
			}
		case <-c.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(messageChangeDebounce)
		}
	}
}

func (c *messageChangeCoordinator) scan(change messageChangeSnapshot) error {
	page, err := c.db.GetSessions("", 0, 0)
	c.mu.Lock()
	c.stats.SessionQueries++
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if page == nil {
		return nil
	}
	sessions := append([]*model.Session(nil), page.Items...)
	// The session query is already a consistent all-row snapshot. Process the
	// newest conversations first so a busy account does not delay the message
	// which triggered this filesystem burst.
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i] == nil || sessions[j] == nil {
			return sessions[i] != nil && sessions[j] == nil
		}
		return sessions[i].NTime.After(sessions[j].NTime)
	})
	paths := change.paths()
	targetedDB, targeted := c.db.(targetedMessageChangeDatabase)
	targeted = targeted && len(paths) > 0
	targetedTalkers := make(map[string]struct{})
	if targeted {
		candidates := make(map[string]model.MessageCursor, len(sessions))
		for _, session := range sessions {
			if session == nil {
				continue
			}
			talker := strings.TrimSpace(session.UserName)
			if talker == "" {
				continue
			}
			if _, duplicate := candidates[talker]; duplicate {
				continue
			}
			candidates[talker] = c.cursor(talker)
		}
		resolved, resolveErr := targetedDB.ResolveChangedMessageTalkers(paths, candidates)
		if resolveErr != nil {
			return resolveErr
		}
		for _, talker := range resolved {
			targetedTalkers[strings.TrimSpace(talker)] = struct{}{}
		}
		c.mu.Lock()
		c.stats.TargetedScans++
		c.stats.TargetedTalkers += uint64(len(targetedTalkers))
		c.mu.Unlock()
	}
	if !targeted || change.generic > 0 {
		c.mu.Lock()
		c.stats.GenericScans++
		c.mu.Unlock()
	}
	var scanErrors []error
	seenTalkers := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		if session == nil {
			continue
		}
		talker := strings.TrimSpace(session.UserName)
		if talker == "" {
			continue
		}
		if _, duplicate := seenTalkers[talker]; duplicate {
			continue
		}
		seenTalkers[talker] = struct{}{}
		cursor := c.cursor(talker)
		useTargeted := false
		if targeted {
			if _, changed := targetedTalkers[talker]; changed {
				useTargeted = true
			} else if change.generic == 0 ||
				(!session.NTime.IsZero() && session.NTime.Unix() < cursor.Timestamp) {
				continue
			}
		} else if !session.NTime.IsZero() && session.NTime.Unix() < cursor.Timestamp {
			continue
		}
		for {
			var messages []*model.Message
			if useTargeted {
				messages, err = targetedDB.GetMessagesAfterFiles(talker, cursor, messageChangePageSize, paths)
			} else {
				messages, err = c.db.GetMessagesAfter(talker, cursor, messageChangePageSize)
			}
			c.mu.Lock()
			c.stats.MessageQueries++
			c.mu.Unlock()
			if err != nil {
				scanErrors = append(scanErrors, fmt.Errorf("%s: %w", talker, err))
				break
			}
			if len(messages) == 0 {
				break
			}
			ordered := make([]*model.Message, 0, len(messages))
			for _, message := range messages {
				if message == nil || !cursor.BeforeMessage(message) {
					continue
				}
				ordered = append(ordered, message)
			}
			if len(ordered) == 0 {
				break
			}
			if err := c.publish(talker, ordered); err != nil {
				return err
			}
			last := ordered[len(ordered)-1]
			cursor = model.MessageCursor{Timestamp: last.Time.Unix(), LocalID: last.DBLocalID}
			c.setCursor(talker, cursor)
			if len(messages) < messageChangePageSize {
				break
			}
		}
	}
	return stderrors.Join(scanErrors...)
}

func (c *messageChangeCoordinator) cursor(talker string) model.MessageCursor {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cursor, ok := c.cursors[talker]; ok {
		return cursor
	}
	// Include the current second after startup while excluding older history.
	cursor := model.MessageCursor{Timestamp: c.startedAt.Unix() - 1, LocalID: math.MaxInt64}
	c.cursors[talker] = cursor
	return cursor
}

func (c *messageChangeCoordinator) setCursor(talker string, cursor model.MessageCursor) {
	c.mu.Lock()
	c.cursors[talker] = cursor
	c.mu.Unlock()
}

func (c *messageChangeCoordinator) publish(talker string, messages []*model.Message) error {
	if len(messages) == 0 {
		return nil
	}
	now := time.Now()
	c.mu.Lock()
	c.nextSequence++
	batch := ports.MessageChangeBatch{
		Sequence:   c.nextSequence,
		Talker:     talker,
		Messages:   append([]*model.Message(nil), messages...),
		DetectedAt: now,
	}
	c.replay = append(c.replay, batch)
	c.replayMessages += len(messages)
	c.trimReplayLocked(now)
	subscriptions := make([]*messageChangeSubscription, 0, len(c.subscriptions))
	for _, subscription := range c.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	c.stats.PublishedBatches++
	c.stats.PublishedMessages += uint64(len(messages))
	c.stats.LastPublishAt = now.Format(time.RFC3339Nano)
	last := messages[len(messages)-1].Time
	if !last.IsZero() {
		lag := now.Sub(last)
		if lag < 0 {
			lag = 0
		}
		c.stats.LastLagMS = lag.Milliseconds()
	}
	c.mu.Unlock()

	for _, subscription := range subscriptions {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-subscription.ctx.Done():
		case subscription.queue <- batch:
		}
	}
	return nil
}

func (c *messageChangeCoordinator) trimReplayLocked(now time.Time) {
	for len(c.replay) > 0 {
		tooMany := len(c.replay) > messageChangeReplayBatches || c.replayMessages > messageChangeReplayMessages
		tooOld := now.Sub(c.replay[0].DetectedAt) > messageChangeReplayAge
		if !tooMany && !tooOld {
			break
		}
		c.replayMessages -= len(c.replay[0].Messages)
		c.replay = c.replay[1:]
	}
}

func (c *messageChangeCoordinator) Subscribe(
	name string,
	since time.Time,
	handler ports.MessageChangeHandler,
) (func(), error) {
	if c == nil || handler == nil {
		return nil, fmt.Errorf("invalid message change subscription")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "consumer"
	}
	ctx, cancel := context.WithCancel(c.ctx)
	subscription := &messageChangeSubscription{
		name:    name,
		handler: handler,
		ctx:     ctx,
		cancel:  cancel,
		queue:   make(chan ports.MessageChangeBatch, messageChangeReplayBatches+64),
		done:    make(chan struct{}),
	}
	c.mu.Lock()
	c.nextSubID++
	subscription.id = c.nextSubID
	c.subscriptions[subscription.id] = subscription
	for _, batch := range c.replay {
		if since.IsZero() || !batch.DetectedAt.Before(since) {
			subscription.queue <- batch
		}
	}
	c.stats.Subscribers = len(c.subscriptions)
	c.mu.Unlock()
	c.wg.Add(1)
	go c.runSubscription(subscription)

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subscriptions, subscription.id)
			c.stats.Subscribers = len(c.subscriptions)
			c.mu.Unlock()
			subscription.cancel()
			<-subscription.done
		})
	}, nil
}

func (c *messageChangeCoordinator) runSubscription(subscription *messageChangeSubscription) {
	defer c.wg.Done()
	defer close(subscription.done)
	for {
		select {
		case <-subscription.ctx.Done():
			return
		case batch := <-subscription.queue:
			delay := 100 * time.Millisecond
			for {
				err := subscription.handler(subscription.ctx, batch)
				if err == nil {
					break
				}
				c.mu.Lock()
				c.stats.DeliveryRetries++
				c.stats.LastError = subscription.name + ": " + err.Error()
				c.mu.Unlock()
				timer := time.NewTimer(delay)
				select {
				case <-subscription.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				delay *= 2
				if delay > 5*time.Second {
					delay = 5 * time.Second
				}
			}
		}
	}
}

func (c *messageChangeCoordinator) Stats() ports.MessageChangeStats {
	if c == nil {
		return ports.MessageChangeStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.stats
	stats.BufferedBatches = len(c.replay)
	stats.BufferedMessages = c.replayMessages
	stats.PendingSourceFiles = len(c.pendingFiles)
	stats.PendingDeliveries = 0
	for _, subscription := range c.subscriptions {
		stats.PendingDeliveries += len(subscription.queue)
	}
	return stats
}
