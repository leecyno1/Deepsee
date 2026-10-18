package messagehook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

type eventTestConfig struct {
	account    string
	workDir    string
	runtimeDir string
	httpAddr   string
	hook       *conf.MessageHook
}

func (c eventTestConfig) GetMessageHook() *conf.MessageHook { return c.hook }
func (c eventTestConfig) GetDataDir() string                { return "" }
func (c eventTestConfig) GetWorkDir() string                { return c.workDir }
func (c eventTestConfig) GetRuntimeDir() string             { return c.runtimeDir }
func (c eventTestConfig) GetHTTPAddr() string               { return c.httpAddr }
func (c eventTestConfig) GetAccount() string                { return c.account }

func TestMediaDownloadDirectoryUsesWorkRoot(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "workspaces", "wxid_example")
	s := &Service{conf: eventTestConfig{
		account:    "wxid_example",
		workDir:    workDir,
		runtimeDir: root,
	}}
	got, err := s.ensureMediaDownloadDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workDir, "cache", "weixin_media")
	if got != want {
		t.Fatalf("media directory = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("media directory permissions = %o, want 700", info.Mode().Perm())
	}
}

func TestSessionInForwardWhitelistMatchesIDOrDisplayName(t *testing.T) {
	contacts := map[string]struct{}{"wxid_friend": {}}
	chatrooms := map[string]struct{}{"项目群": {}}

	if !sessionInForwardWhitelist("wxid_friend", "好友", contacts, chatrooms) {
		t.Fatal("contact ID should match")
	}
	if !sessionInForwardWhitelist("123@chatroom", "项目群", contacts, chatrooms) {
		t.Fatal("chatroom display name should match")
	}
	if sessionInForwardWhitelist("other@chatroom", "其他群", contacts, chatrooms) {
		t.Fatal("unlisted session should be skipped")
	}
}

func TestScanStopsBeforeDatabaseWorkWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := &Service{}
	_, _, _, err := service.scanOnceContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan returned %v", err)
	}
}

func TestFailedPostDeliveryIsRetried(t *testing.T) {
	requests := 0
	var idempotencyKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		idempotencyKeys = append(idempotencyKeys, r.Header.Get("Idempotency-Key"))
		if requests == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	storePath := filepath.Join(t.TempDir(), "runtime", "hook.db")
	store, err := openOutboxStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		httpClient: server.Client(),
		store:      store,
	}
	evt := Event{EventID: "stable-event", Talker: "room", TriggerSeq: 7, RuleType: "keyword", RuleLabel: "build"}
	if err := store.Commit("account", "room", model.MessageCursor{Timestamp: 1, LocalID: 7}, &evt, []deliveryTarget{{Name: "post", Destination: server.URL}}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatalf("claim first delivery: job=%#v err=%v", job, err)
	}
	result := service.deliverJob(*job)
	if result.Success {
		t.Fatal("first delivery should fail")
	}
	if err := store.Complete(*job, result, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openOutboxStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service.store = store
	job, err = store.Claim(time.Now().Add(maxPostRetryDelay))
	if err != nil || job == nil {
		t.Fatalf("claim persisted retry: job=%#v err=%v", job, err)
	}
	result = service.deliverJob(*job)
	if !result.Success {
		t.Fatalf("retry failed: %#v", result)
	}
	if err := store.Complete(*job, result, time.Now()); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	for _, key := range idempotencyKeys {
		if key != evt.EventID {
			t.Fatalf("idempotency key = %q, want %q", key, evt.EventID)
		}
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingDeliveries != 0 {
		t.Fatalf("pending deliveries = %d, want 0", stats.PendingDeliveries)
	}
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].Deliveries) != 1 || !events[0].Deliveries[0].Success {
		t.Fatalf("stored events = %#v", events)
	}
}

func TestPersistentDeliveryCanBeCanceledAndRetried(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().Truncate(time.Second)
	event := Event{EventID: "controllable-event", Talker: "room", TriggerSeq: 17}
	if err := store.Commit(
		"account",
		"room",
		model.MessageCursor{Timestamp: now.Unix(), LocalID: 17},
		&event,
		[]deliveryTarget{{Name: "post", Destination: "https://example.invalid/hook"}},
	); err != nil {
		t.Fatal(err)
	}

	affected, err := store.CancelEvent(event.EventID, "post", now)
	if err != nil || affected != 1 {
		t.Fatalf("cancel affected=%d err=%v", affected, err)
	}
	if job, err := store.Claim(now.Add(time.Hour)); err != nil || job != nil {
		t.Fatalf("canceled delivery was claimable: job=%#v err=%v", job, err)
	}

	affected, err = store.RetryEvent(event.EventID, "post", now.Add(time.Second))
	if err != nil || affected != 1 {
		t.Fatalf("retry affected=%d err=%v", affected, err)
	}
	job, err := store.Claim(now.Add(2 * time.Second))
	if err != nil || job == nil {
		t.Fatalf("retried delivery was not claimable: job=%#v err=%v", job, err)
	}

	affected, err = store.CancelEvent(event.EventID, "post", now.Add(3*time.Second))
	if err != nil || affected != 1 {
		t.Fatalf("cancel delivering affected=%d err=%v", affected, err)
	}
	if err := store.Complete(*job, DeliveryResult{Target: "post", Status: "sent", Success: true}, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].Deliveries) != 1 || events[0].Deliveries[0].Status != outboxStatusCanceled {
		t.Fatalf("late completion overwrote cancellation: %#v", events)
	}

	affected, err = store.RetryEvent(event.EventID, "post", now.Add(5*time.Second))
	if err != nil || affected != 1 {
		t.Fatalf("second retry affected=%d err=%v", affected, err)
	}
	job, err = store.Claim(now.Add(6 * time.Second))
	if err != nil || job == nil {
		t.Fatalf("second retry was not claimable: job=%#v err=%v", job, err)
	}
	if err := store.Complete(*job, DeliveryResult{Target: "post", Status: "sent", Success: true}, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	events, err = store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if got := events[0].Deliveries[0]; !got.Success || got.Status != outboxStatusSent || got.Attempts != 1 {
		t.Fatalf("retried completion = %#v", got)
	}
}

func TestPersistentDeliveriesSupportBatchRetryAndDelete(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().Truncate(time.Second)
	for index, eventID := range []string{"batch-a", "batch-b"} {
		event := Event{EventID: eventID, Talker: "room", TriggerSeq: int64(index + 1)}
		if err := store.Commit("account", "room", model.MessageCursor{Timestamp: now.Unix(), LocalID: int64(index + 1)}, &event, []deliveryTarget{{Name: "post"}}); err != nil {
			t.Fatal(err)
		}
		if affected, err := store.CancelEvent(eventID, "", now); err != nil || affected != 1 {
			t.Fatalf("cancel %s affected=%d err=%v", eventID, affected, err)
		}
	}
	if affected, err := store.RetryEvents([]string{"batch-a", "batch-b", "batch-a"}, "", now.Add(time.Second)); err != nil || affected != 2 {
		t.Fatalf("batch retry affected=%d err=%v", affected, err)
	}
	if affected, err := store.DeleteEvents([]string{"batch-a", "batch-b"}, ""); err != nil || affected != 2 {
		t.Fatalf("batch delete affected=%d err=%v", affected, err)
	}
	stats, err := store.Stats()
	if err != nil || stats.EventCount != 0 || stats.PendingDeliveries != 0 {
		t.Fatalf("stats after batch delete=%#v err=%v", stats, err)
	}
}

func TestOutboxNextDeliveryAtUsesRetryDeadline(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, exists, err := store.NextDeliveryAt(); err != nil || exists {
		t.Fatalf("empty outbox deadline exists=%v err=%v", exists, err)
	}
	event := Event{EventID: "deadline-event", Talker: "room", TriggerSeq: 99}
	if err := store.EnqueueEvent(event, []deliveryTarget{{Name: "post", Destination: "https://example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	next, exists, err := store.NextDeliveryAt()
	if err != nil || !exists || next.After(time.Now()) {
		t.Fatalf("pending deadline=%v exists=%v err=%v", next, exists, err)
	}
	job, err := store.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatalf("claim job=%#v err=%v", job, err)
	}
	completedAt := time.Now()
	if err := store.Complete(*job, DeliveryResult{Target: "post", Status: "failed", Detail: "retry", Success: false}, completedAt); err != nil {
		t.Fatal(err)
	}
	next, exists, err = store.NextDeliveryAt()
	if err != nil || !exists || next.Before(completedAt.Add(postRetryBaseDelay-time.Second)) {
		t.Fatalf("retry deadline=%v exists=%v err=%v", next, exists, err)
	}
}

func TestPersistentDeliveryCanBeDeleted(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{EventID: "delete-event", Talker: "room", TriggerSeq: 18}
	if err := store.EnqueueEvent(event, []deliveryTarget{
		{Name: "post", Destination: "https://example.invalid/hook"},
		{Name: "qq"},
	}); err != nil {
		t.Fatal(err)
	}
	affected, err := store.DeleteEvent(event.EventID, "post")
	if err != nil || affected != 1 {
		t.Fatalf("delete target affected=%d err=%v", affected, err)
	}
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].Deliveries) != 1 || events[0].Deliveries[0].Target != "qq" {
		t.Fatalf("remaining deliveries = %#v", events)
	}
	affected, err = store.DeleteEvent(event.EventID, "")
	if err != nil || affected != 1 {
		t.Fatalf("delete event affected=%d err=%v", affected, err)
	}
	events, err = store.RecentEvents(10)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after delete = %#v err=%v", events, err)
	}
}

func TestPostDeliveryStopsImmediatelyWithServiceContext(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()

	service := &Service{httpClient: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	resultDone := make(chan DeliveryResult, 1)
	go func() {
		resultDone <- service.deliverPostWithKeyContext(ctx, server.URL, Event{EventID: "shutdown"}, "shutdown")
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("delivery request did not start")
	}
	cancel()
	select {
	case result := <-resultDone:
		if result.Success {
			t.Fatalf("canceled delivery unexpectedly succeeded: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled delivery kept project shutdown waiting")
	}
}

func TestCancelEventStopsActivePersistentDelivery(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()

	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{EventID: "cancel-active", Talker: "room", TriggerSeq: 19}
	if err := store.Commit(
		"account",
		"room",
		model.MessageCursor{Timestamp: 1, LocalID: 19},
		&event,
		[]deliveryTarget{{Name: "post", Destination: server.URL}},
	); err != nil {
		t.Fatal(err)
	}
	service := &Service{httpClient: server.Client(), store: store}
	drained := make(chan error, 1)
	go func() {
		drained <- service.drainDeliveries(context.Background())
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("persistent delivery did not start")
	}
	if affected, err := service.CancelEvent(event.EventID, "post"); err != nil || affected != 1 {
		t.Fatalf("cancel affected=%d err=%v", affected, err)
	}
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("active delivery remained blocked after cancellation")
	}
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].Deliveries) != 1 || events[0].Deliveries[0].Status != outboxStatusCanceled {
		t.Fatalf("canceled delivery state = %#v", events)
	}
}

func TestServiceCloseDoesNotWaitForBlockedWebhookTimeout(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()

	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "shutdown-event", Talker: "room", TriggerSeq: 9}
	if err := store.Commit(
		"account",
		"room",
		model.MessageCursor{Timestamp: 1, LocalID: 9},
		&event,
		[]deliveryTarget{{Name: "post", Destination: server.URL}},
	); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		conf:         eventTestConfig{hook: &conf.MessageHook{}},
		httpClient:   server.Client(),
		store:        store,
		scanWake:     make(chan struct{}, 1),
		deliveryWake: make(chan struct{}, 1),
		runDone:      make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	go service.Run(ctx)
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("delivery worker did not start blocked webhook")
	}

	started := time.Now()
	cancel()
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("service close waited for HTTP client timeout: %s", elapsed)
	}
}

func TestClearEventsHidesHistoryWithoutDroppingPendingDelivery(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{EventID: "pending-event", Talker: "room", TriggerSeq: 9}
	if err := store.Commit("account", "room", model.MessageCursor{Timestamp: 2, LocalID: 9}, &event, []deliveryTarget{{Name: "post", Destination: "https://example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.ClearEvents()
	if err != nil || deleted != 1 {
		t.Fatalf("clear = %d, %v", deleted, err)
	}
	events, err := store.RecentEvents(10)
	if err != nil || len(events) != 0 {
		t.Fatalf("visible events after clear = %#v, %v", events, err)
	}
	job, err := store.Claim(time.Now())
	if err != nil || job == nil || job.EventID != event.EventID {
		t.Fatalf("pending delivery was removed: job=%#v err=%v", job, err)
	}
}

func TestLegacyEventFileMigratesIntoSQLiteAndIsRemoved(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(runtimeDir, "hook_events.json")
	legacyEvent := Event{
		ID:         41,
		CreatedAt:  time.Unix(100, 0).Format(time.RFC3339),
		Talker:     "room@chatroom",
		TriggerSeq: 41,
		MatchedRules: []RuleMatch{{
			RuleType: "keyword",
			Keyword:  "alert",
		}},
	}
	data, err := json.Marshal([]Event{legacyEvent})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := openOutboxStore(filepath.Join(runtimeDir, "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy file still exists after migration: %v", err)
	}
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TriggerSeq != legacyEvent.TriggerSeq {
		t.Fatalf("migrated events = %#v", events)
	}
	if len(events[0].MatchedRules) != 1 || events[0].MatchedRules[0].Keyword != "alert" {
		t.Fatalf("matched rules were not preserved: %#v", events[0].MatchedRules)
	}
}

func TestDeliveryMovesToDeadLetterAfterBoundedRetries(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{EventID: "dead-event", Talker: "room", TriggerSeq: 99}
	if err := store.Commit("account", "room", model.MessageCursor{Timestamp: 3, LocalID: 99}, &event, []deliveryTarget{{Name: "post", Destination: "https://example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for attempt := 0; attempt < maxDeliveryAttempts; attempt++ {
		job, err := store.Claim(now.Add(time.Duration(attempt+1) * maxPostRetryDelay))
		if err != nil || job == nil {
			t.Fatalf("claim attempt %d: job=%#v err=%v", attempt+1, job, err)
		}
		if err := store.Complete(*job, DeliveryResult{Target: "post", Status: "failed", Detail: "unavailable"}, now); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingDeliveries != 0 || stats.FailedDeliveries != 1 {
		t.Fatalf("dead-letter stats = %#v", stats)
	}
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].Deliveries) != 1 || events[0].Deliveries[0].Status != outboxStatusDead {
		t.Fatalf("dead-letter event = %#v", events)
	}
}

type scannerTestDB struct {
	sessions      []*model.Session
	messages      map[string][]*model.Message
	getAfterCalls int
}

func (db *scannerTestDB) GetSessions(_ string, limit, offset int) (*ports.Sessions, error) {
	if offset >= len(db.sessions) {
		return &ports.Sessions{}, nil
	}
	if limit <= 0 {
		return &ports.Sessions{Items: db.sessions[offset:]}, nil
	}
	end := offset + limit
	if end > len(db.sessions) {
		end = len(db.sessions)
	}
	return &ports.Sessions{Items: db.sessions[offset:end]}, nil
}

func (db *scannerTestDB) GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error) {
	db.getAfterCalls++
	var out []*model.Message
	for _, message := range db.messages[talker] {
		if cursor.BeforeMessage(message) {
			out = append(out, message)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func TestCursorScannerSkipsInactiveSessionShardReads(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	start := time.Now().Truncate(time.Second)
	db := &scannerTestDB{
		sessions: []*model.Session{{
			UserName: "old-contact",
			NTime:    start.Add(-time.Hour),
		}},
		messages: map[string][]*model.Message{},
	}
	cfg := eventTestConfig{
		account: "wxid_test",
		hook: &conf.MessageHook{
			Keywords:   "alert",
			NotifyMode: "post",
			PostURL:    "https://example.invalid/hook",
		},
	}
	service := &Service{
		conf:         cfg,
		db:           db,
		store:        store,
		deliveryWake: make(chan struct{}, 1),
	}
	if err := store.Activate("wxid_test", start, nil, pushRuleFingerprint(cfg.hook)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	}
	if db.getAfterCalls != 0 {
		t.Fatalf("inactive session caused %d message shard reads", db.getAfterCalls)
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.CursorCount != 1 {
		t.Fatalf("cursor count = %d, want 1", stats.CursorCount)
	}
}

func (db *scannerTestDB) GetMessages(start, end time.Time, talker, _, _ string, _, _ int) ([]*model.Message, error) {
	items := db.messages[talker]
	filtered := make([]*model.Message, 0, len(items))
	for _, message := range items {
		if message == nil || (!start.IsZero() && message.Time.Before(start)) || (!end.IsZero() && message.Time.After(end)) {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered, nil
}

func (db *scannerTestDB) GetMessage(talker string, seq int64) (*model.Message, error) {
	for _, message := range db.messages[talker] {
		if message.Seq == seq {
			return message, nil
		}
	}
	return nil, fmt.Errorf("message not found")
}

func (db *scannerTestDB) GetMedia(string, string) (*model.Media, error) {
	return nil, fmt.Errorf("media not found")
}

func (db *scannerTestDB) SetCallback(string, func(fsnotify.Event) error) error { return nil }

func TestOCRKeywordCreatesDurableImageDelivery(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	message := &model.Message{
		Seq: 31, DBLocalID: 31, Time: time.Now(), Talker: "friend", TalkerName: "好友",
		Sender: "wxid_friend", SenderName: "好友", Type: model.MessageTypeImage,
		Contents: map[string]interface{}{"md5": "image-md5"},
	}
	db := &scannerTestDB{messages: map[string][]*model.Message{"friend": {message}}}
	cfg := eventTestConfig{
		account:  "wxid_owner",
		httpAddr: "127.0.0.1:5030",
		hook: &conf.MessageHook{
			Keywords:    "发票｜告警",
			KeywordMode: conf.HookKeywordModeImageOCR,
			NotifyMode:  "post",
			PostURL:     "https://example.invalid/hook",
			BeforeCount: 0,
			AfterCount:  0,
		},
	}
	service := &Service{
		conf:         cfg,
		db:           db,
		store:        store,
		deliveryWake: make(chan struct{}, 1),
	}
	if err := store.Activate("wxid_owner", message.Time.Add(-time.Second), nil, pushRuleFingerprint(cfg.hook)); err != nil {
		t.Fatal(err)
	}
	matched, err := service.NotifyOCRResult(message, "这是一张发票，号码 123")
	if err != nil || !matched {
		t.Fatalf("OCR match=%v err=%v", matched, err)
	}
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Keyword != "发票" {
		t.Fatalf("OCR events = %#v", events)
	}
	if got := events[0].TriggerMediaURLs; len(got) != 1 || got[0] != "http://127.0.0.1:5030/image/image-md5" {
		t.Fatalf("OCR image URLs = %#v", got)
	}
	if len(events[0].Deliveries) != 1 || events[0].Deliveries[0].Target != "post" {
		t.Fatalf("OCR deliveries = %#v", events[0].Deliveries)
	}
	// The stable message identity prevents duplicate OCR completion callbacks
	// from adding another delivery.
	if _, err := service.NotifyOCRResult(message, "发票"); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats()
	if err != nil || stats.EventCount != 1 || stats.PendingDeliveries != 1 {
		t.Fatalf("deduplicated OCR stats = %#v err=%v", stats, err)
	}
}

func TestKeywordModesSeparateTextAndImageOCR(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	textMessage := &model.Message{
		Seq: 1, DBLocalID: 1, Time: base.Add(time.Second), Talker: "friend",
		Type: model.MessageTypeText, Content: "alert in text",
	}
	imageMessage := &model.Message{
		Seq: 2, DBLocalID: 2, Time: base.Add(2 * time.Second), Talker: "friend",
		Type: model.MessageTypeImage, Content: "alert in raw image payload",
		Contents: map[string]interface{}{"md5": "mode-image"},
	}
	db := &scannerTestDB{
		sessions: []*model.Session{{UserName: "friend", NTime: imageMessage.Time}},
		messages: map[string][]*model.Message{"friend": {textMessage, imageMessage}},
	}
	hook := &conf.MessageHook{
		Keywords: "alert", KeywordMode: conf.HookKeywordModeText,
		NotifyMode: conf.HookNotifyPost, PostURL: "https://example.invalid/hook",
		BeforeCount: 0, AfterCount: 0,
	}
	service := &Service{conf: eventTestConfig{account: "wxid_test", hook: hook}, db: db, store: store, deliveryWake: make(chan struct{}, 1)}
	if err := store.Activate("wxid_test", base, nil, pushRuleFingerprint(hook)); err != nil {
		t.Fatal(err)
	}
	if _, scanned, matched, err := service.scanOnce(); err != nil || scanned != 2 || matched != 1 {
		t.Fatalf("text mode scan=%d/%d err=%v", scanned, matched, err)
	}
	if matchedOCR, err := service.NotifyOCRResult(imageMessage, "alert from OCR"); err != nil || matchedOCR {
		t.Fatalf("text mode OCR match=%v err=%v", matchedOCR, err)
	}

	hook.KeywordMode = conf.HookKeywordModeImageOCR
	if _, scanned, matched, err := service.scanOnce(); err != nil || scanned != 0 || matched != 0 {
		t.Fatalf("mode switch replayed history: %d/%d err=%v", scanned, matched, err)
	}
	if matchedOCR, err := service.NotifyOCRResult(imageMessage, "alert from old image"); err != nil || matchedOCR {
		t.Fatalf("old image matched after mode switch=%v err=%v", matchedOCR, err)
	}
	newImage := *imageMessage
	newImage.Seq = 3
	newImage.DBLocalID = 3
	newImage.Time = time.Now().Add(time.Second)
	newImage.Contents = map[string]interface{}{"md5": "new-mode-image"}
	if matchedOCR, err := service.NotifyOCRResult(&newImage, "alert from new image"); err != nil || !matchedOCR {
		t.Fatalf("new image OCR match=%v err=%v", matchedOCR, err)
	}
	stats, err := store.Stats()
	if err != nil || stats.EventCount != 2 {
		t.Fatalf("events=%#v err=%v", stats, err)
	}
}

func TestMixedKeywordModeMatchesTextAndImageOCR(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	textMessage := &model.Message{
		Seq: 1, DBLocalID: 1, Time: base.Add(time.Second), Talker: "friend",
		Type: model.MessageTypeText, Content: "alert in text",
	}
	imageMessage := &model.Message{
		Seq: 2, DBLocalID: 2, Time: base.Add(2 * time.Second), Talker: "friend",
		Type: model.MessageTypeImage, Contents: map[string]interface{}{"md5": "mixed-image"},
	}
	db := &scannerTestDB{
		sessions: []*model.Session{{UserName: "friend", NTime: imageMessage.Time}},
		messages: map[string][]*model.Message{"friend": {textMessage, imageMessage}},
	}
	hook := &conf.MessageHook{
		Keywords: "alert", KeywordMode: conf.HookKeywordModeMixed,
		NotifyMode: conf.HookNotifyPost, PostURL: "https://example.invalid/hook",
	}
	service := &Service{conf: eventTestConfig{account: "wxid_test", hook: hook}, db: db, store: store, deliveryWake: make(chan struct{}, 1)}
	if err := store.Activate("wxid_test", base, nil, pushRuleFingerprint(hook)); err != nil {
		t.Fatal(err)
	}
	if _, scanned, matched, err := service.scanOnce(); err != nil || scanned != 2 || matched != 1 {
		t.Fatalf("mixed text scan=%d/%d err=%v", scanned, matched, err)
	}
	if matchedOCR, err := service.NotifyOCRResult(imageMessage, "alert from OCR"); err != nil || !matchedOCR {
		t.Fatalf("mixed image OCR match=%v err=%v", matchedOCR, err)
	}
	stats, err := store.Stats()
	if err != nil || stats.EventCount != 2 {
		t.Fatalf("mixed events=%#v err=%v", stats, err)
	}
}

func TestCursorScannerPagesAllSessionsAndBurstMessagesWithoutDuplicates(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	db := &scannerTestDB{messages: map[string][]*model.Message{}}
	for index := 0; index < 301; index++ {
		talker := fmt.Sprintf("room-%03d@chatroom", index)
		db.sessions = append(db.sessions, &model.Session{UserName: talker})
	}
	targetTalker := db.sessions[len(db.sessions)-1].UserName
	for index := 1; index <= 501; index++ {
		ts := start.Add(time.Duration(index/100) * time.Second)
		db.messages[targetTalker] = append(db.messages[targetTalker], &model.Message{
			Seq:       int64(index),
			DBLocalID: int64(index),
			Time:      ts,
			Talker:    targetTalker,
			Type:      model.MessageTypeText,
			Content:   "alert",
		})
	}
	cfg := eventTestConfig{
		account: "wxid_test",
		hook: &conf.MessageHook{
			Keywords:    "alert",
			NotifyMode:  "post",
			PostURL:     "https://example.invalid/hook",
			BeforeCount: 0,
			AfterCount:  0,
		},
	}
	service := &Service{
		conf:         cfg,
		db:           db,
		store:        store,
		deliveryWake: make(chan struct{}, 1),
	}
	if err := store.Activate("wxid_test", start.Add(-time.Second), nil, pushRuleFingerprint(cfg.hook)); err != nil {
		t.Fatal(err)
	}
	sessions, messages, matched, err := service.scanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 301 || messages != 501 || matched != 501 {
		t.Fatalf("scan = sessions:%d messages:%d matched:%d", sessions, messages, matched)
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 501 || stats.PendingDeliveries != 501 || stats.CursorCount != 301 {
		t.Fatalf("store stats = %#v", stats)
	}
	_, messages, matched, err = service.scanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if messages != 0 || matched != 0 {
		t.Fatalf("second scan replayed messages: messages=%d matched=%d", messages, matched)
	}
}

func TestRealtimePushOnlyProcessesMessagesAddedAfterEachEnable(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	talker := "friend"
	hook := &conf.MessageHook{}
	cfg := eventTestConfig{account: "wxid_test", hook: hook}
	db := &scannerTestDB{
		sessions: []*model.Session{{UserName: talker, NTime: base}},
		messages: map[string][]*model.Message{
			talker: {{
				Seq: 1, DBLocalID: 1, Time: base, Talker: talker,
				Type: model.MessageTypeText, Content: "existing",
			}},
		},
	}
	service := &Service{
		conf: cfg, db: db, store: store,
		deliveryWake: make(chan struct{}, 1),
	}

	// Disabled state is persisted, making the next active scan a new epoch.
	if _, _, _, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	}
	hook.ForwardAll = true
	hook.NotifyMode = conf.HookNotifyPost
	hook.PostURL = "https://example.invalid/hook"
	if _, scanned, matched, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	} else if scanned != 0 || matched != 0 {
		t.Fatalf("enable replayed existing content: scanned=%d matched=%d", scanned, matched)
	}

	firstNew := &model.Message{
		Seq: 2, DBLocalID: 2, Time: base.Add(time.Second), Talker: talker,
		Type: model.MessageTypeText, Content: "first new",
	}
	db.messages[talker] = append(db.messages[talker], firstNew)
	db.sessions[0].NTime = firstNew.Time
	if _, scanned, matched, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	} else if scanned != 1 || matched != 1 {
		t.Fatalf("new content scan = %d/%d, want 1/1", scanned, matched)
	}

	hook.ForwardAll = false
	if _, _, _, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	}
	whileOff := &model.Message{
		Seq: 3, DBLocalID: 3, Time: base.Add(2 * time.Second), Talker: talker,
		Type: model.MessageTypeText, Content: "while off",
	}
	db.messages[talker] = append(db.messages[talker], whileOff)
	db.sessions[0].NTime = whileOff.Time
	hook.ForwardAll = true
	if _, scanned, matched, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	} else if scanned != 0 || matched != 0 {
		t.Fatalf("re-enable replayed disabled interval: scanned=%d matched=%d", scanned, matched)
	}

	secondNew := &model.Message{
		Seq: 4, DBLocalID: 4, Time: base.Add(3 * time.Second), Talker: talker,
		Type: model.MessageTypeText, Content: "second new",
	}
	db.messages[talker] = append(db.messages[talker], secondNew)
	db.sessions[0].NTime = secondNew.Time
	if _, scanned, matched, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	} else if scanned != 1 || matched != 1 {
		t.Fatalf("post re-enable scan = %d/%d, want 1/1", scanned, matched)
	}

	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 2 {
		t.Fatalf("event count = %d, want only the two post-enable messages", stats.EventCount)
	}
}

func TestActivationUsesSavedEffectiveTimeInsteadOfDelayedScanTime(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	effectiveAt := base.Add(2 * time.Second)
	oldMessage := &model.Message{Seq: 1, DBLocalID: 1, Time: base, Talker: "friend", Type: model.MessageTypeText, Content: "alert old"}
	newMessage := &model.Message{Seq: 2, DBLocalID: 2, Time: base.Add(3 * time.Second), Talker: "friend", Type: model.MessageTypeText, Content: "alert new"}
	hook := &conf.MessageHook{
		Keywords: "alert", KeywordMode: conf.HookKeywordModeText, EffectiveAt: effectiveAt.Unix(),
		NotifyMode: conf.HookNotifyPost, PostURL: "https://example.invalid/hook", BeforeCount: 0, AfterCount: 0,
	}
	db := &scannerTestDB{
		sessions: []*model.Session{{UserName: "friend", NTime: newMessage.Time}},
		messages: map[string][]*model.Message{"friend": {oldMessage, newMessage}},
	}
	service := &Service{conf: eventTestConfig{account: "wxid_test", hook: hook}, db: db, store: store, deliveryWake: make(chan struct{}, 1)}
	if _, scanned, matched, err := service.scanOnce(); err != nil || scanned != 0 || matched != 0 {
		t.Fatalf("activation scan=%d/%d err=%v", scanned, matched, err)
	}
	if _, scanned, matched, err := service.scanOnce(); err != nil || scanned != 1 || matched != 1 {
		t.Fatalf("post-effective catch-up=%d/%d err=%v", scanned, matched, err)
	}
	events, err := store.RecentEvents(10)
	if err != nil || len(events) != 1 || events[0].TriggerContent != "alert new" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestActivePushStateSurvivesServiceRestart(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "runtime", "hook.db")
	store, err := openOutboxStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	talker := "friend"
	hook := &conf.MessageHook{
		ForwardAll: true, NotifyMode: conf.HookNotifyPost,
		PostURL: "https://example.invalid/hook",
	}
	cfg := eventTestConfig{account: "wxid_test", hook: hook}
	db := &scannerTestDB{
		sessions: []*model.Session{{UserName: talker, NTime: base}},
		messages: map[string][]*model.Message{
			talker: {{
				Seq: 1, DBLocalID: 1, Time: base, Talker: talker,
				Type: model.MessageTypeText, Content: "existing",
			}},
		},
	}
	service := &Service{conf: cfg, db: db, store: store, deliveryWake: make(chan struct{}, 1)}
	if _, _, _, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	duringRestart := &model.Message{
		Seq: 2, DBLocalID: 2, Time: base.Add(time.Second), Talker: talker,
		Type: model.MessageTypeText, Content: "during restart",
	}
	db.messages[talker] = append(db.messages[talker], duringRestart)
	db.sessions[0].NTime = duringRestart.Time
	store, err = openOutboxStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service = &Service{conf: cfg, db: db, store: store, deliveryWake: make(chan struct{}, 1)}
	if _, scanned, matched, err := service.scanOnce(); err != nil {
		t.Fatal(err)
	} else if scanned != 1 || matched != 1 {
		t.Fatalf("restart scan = %d/%d, want persisted active cursor to continue", scanned, matched)
	}
}

func TestChangingActivePushRulesStartsANewCurrentTimeEpoch(t *testing.T) {
	store, err := openOutboxStore(filepath.Join(t.TempDir(), "runtime", "hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	hook := &conf.MessageHook{
		ForwardContacts: "friend-a", NotifyMode: conf.HookNotifyPost,
		PostURL: "https://example.invalid/hook",
	}
	cfg := eventTestConfig{account: "wxid_test", hook: hook}
	db := &scannerTestDB{
		sessions: []*model.Session{
			{UserName: "friend-a", NTime: base},
			{UserName: "friend-b", NTime: base},
		},
		messages: map[string][]*model.Message{
			"friend-a": {{Seq: 1, DBLocalID: 1, Time: base, Talker: "friend-a", Type: model.MessageTypeText, Content: "a old"}},
			"friend-b": {{Seq: 2, DBLocalID: 2, Time: base, Talker: "friend-b", Type: model.MessageTypeText, Content: "b old"}},
		},
	}
	service := &Service{conf: cfg, db: db, store: store, deliveryWake: make(chan struct{}, 1)}
	if _, scanned, matched, err := service.scanOnce(); err != nil || scanned != 0 || matched != 0 {
		t.Fatalf("initial activation = %d/%d err=%v", scanned, matched, err)
	}
	beforeRuleChange := &model.Message{
		Seq: 3, DBLocalID: 3, Time: base.Add(time.Second), Talker: "friend-b",
		Type: model.MessageTypeText, Content: "arrived before friend-b rule",
	}
	db.messages["friend-b"] = append(db.messages["friend-b"], beforeRuleChange)
	db.sessions[1].NTime = beforeRuleChange.Time
	hook.ForwardContacts = "friend-b"
	if _, scanned, matched, err := service.scanOnce(); err != nil || scanned != 0 || matched != 0 {
		t.Fatalf("rule change replayed earlier row: %d/%d err=%v", scanned, matched, err)
	}
	afterRuleChange := &model.Message{
		Seq: 4, DBLocalID: 4, Time: time.Now().Add(time.Second), Talker: "friend-b",
		Type: model.MessageTypeText, Content: "arrived after friend-b rule",
	}
	db.messages["friend-b"] = append(db.messages["friend-b"], afterRuleChange)
	db.sessions[1].NTime = afterRuleChange.Time
	if _, scanned, matched, err := service.scanOnce(); err != nil || scanned != 1 || matched != 1 {
		t.Fatalf("post-rule-change scan = %d/%d err=%v", scanned, matched, err)
	}
}

func TestBuildEventIncludesOwnerAndAtUserList(t *testing.T) {
	service := &Service{conf: eventTestConfig{account: "wxid_owner_abcd"}}
	trigger := &model.Message{
		Seq:        42,
		Time:       time.Unix(123, 0),
		Talker:     "room@chatroom",
		Sender:     "sender",
		Type:       1,
		AtUserList: []string{"wxid_a", "wxid_b"},
	}
	cfg := &conf.MessageHook{BeforeCount: 0, AfterCount: 0}
	event := service.buildEventForRules(trigger, []RuleMatch{{
		RuleType:  "keyword",
		RuleLabel: "rule",
		Keyword:   "hello",
	}}, "hello", cfg)
	if event.OwnerWxid != "wxid_owner" {
		t.Fatalf("OwnerWxid = %q, want wxid_owner", event.OwnerWxid)
	}
	if !reflect.DeepEqual(event.AtUserList, trigger.AtUserList) {
		t.Fatalf("AtUserList = %#v, want %#v", event.AtUserList, trigger.AtUserList)
	}
}
