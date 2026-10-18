package database

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

type messageChangeDBFixture struct {
	mu          sync.Mutex
	sessions    []*model.Session
	messages    map[string][]*model.Message
	callbacks   map[string][]func(fsnotify.Event) error
	messageRead int
	targetRead  int
}

func (f *messageChangeDBFixture) GetSessions(_ string, limit, offset int) (*ports.Sessions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if offset >= len(f.sessions) {
		return &ports.Sessions{Items: []*model.Session{}}, nil
	}
	if limit <= 0 {
		return &ports.Sessions{Items: append([]*model.Session(nil), f.sessions[offset:]...)}, nil
	}
	end := offset + limit
	if end > len(f.sessions) {
		end = len(f.sessions)
	}
	return &ports.Sessions{Items: append([]*model.Session(nil), f.sessions[offset:end]...)}, nil
}

func (f *messageChangeDBFixture) GetMessagesAfter(talker string, cursor model.MessageCursor, limit int) ([]*model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messageRead++
	out := make([]*model.Message, 0, limit)
	for _, message := range f.messages[talker] {
		if cursor.BeforeMessage(message) {
			out = append(out, message)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (f *messageChangeDBFixture) SubscribeCallback(group string, callback func(fsnotify.Event) error) (func(), error) {
	f.mu.Lock()
	f.callbacks[group] = append(f.callbacks[group], callback)
	f.mu.Unlock()
	return func() {}, nil
}

func (f *messageChangeDBFixture) ResolveChangedMessageTalkers(
	_ []string,
	candidates map[string]model.MessageCursor,
) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resolved := make([]string, 0, len(candidates))
	for talker, cursor := range candidates {
		for _, message := range f.messages[talker] {
			if cursor.BeforeMessage(message) {
				resolved = append(resolved, talker)
				break
			}
		}
	}
	return resolved, nil
}

func (f *messageChangeDBFixture) GetMessagesAfterFiles(
	talker string,
	cursor model.MessageCursor,
	limit int,
	_ []string,
) ([]*model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targetRead++
	out := make([]*model.Message, 0, limit)
	for _, message := range f.messages[talker] {
		if cursor.BeforeMessage(message) {
			out = append(out, message)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (f *messageChangeDBFixture) publish(group string) {
	f.publishEvent(group, fsnotify.Event{Op: fsnotify.Write})
}

func (f *messageChangeDBFixture) publishEvent(group string, event fsnotify.Event) {
	f.mu.Lock()
	callbacks := append([]func(fsnotify.Event) error(nil), f.callbacks[group]...)
	f.mu.Unlock()
	for _, callback := range callbacks {
		_ = callback(event)
	}
}

func TestMessageChangeCoordinatorReadsOnceAndFansOut(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fixture := &messageChangeDBFixture{
		sessions:  []*model.Session{{UserName: "friend", NTime: now.Add(-time.Hour)}},
		messages:  map[string][]*model.Message{"friend": {}},
		callbacks: make(map[string][]func(fsnotify.Event) error),
	}
	coordinator := newMessageChangeCoordinator(fixture)
	if err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}
	defer coordinator.Stop()
	first := make(chan ports.MessageChangeBatch, 1)
	second := make(chan ports.MessageChangeBatch, 1)
	cancelFirst, err := coordinator.Subscribe("first", time.Now(), func(_ context.Context, batch ports.MessageChangeBatch) error {
		first <- batch
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancelFirst()
	cancelSecond, err := coordinator.Subscribe("second", time.Now(), func(_ context.Context, batch ports.MessageChangeBatch) error {
		second <- batch
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancelSecond()

	message := &model.Message{Talker: "friend", Time: now.Add(time.Second), DBLocalID: 7, Content: "new"}
	fixture.mu.Lock()
	fixture.messages["friend"] = append(fixture.messages["friend"], message)
	fixture.sessions[0].NTime = message.Time
	readsBefore := fixture.messageRead
	fixture.mu.Unlock()
	fixture.publish("message")

	for index, channel := range []<-chan ports.MessageChangeBatch{first, second} {
		select {
		case batch := <-channel:
			if len(batch.Messages) != 1 || batch.Messages[0].DBLocalID != 7 {
				t.Fatalf("consumer %d batch=%#v", index, batch)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("consumer %d did not receive shared batch", index)
		}
	}
	fixture.mu.Lock()
	reads := fixture.messageRead - readsBefore
	fixture.mu.Unlock()
	if reads != 1 {
		t.Fatalf("message shard reads=%d want=1 for two consumers", reads)
	}
	stats := coordinator.Stats()
	if stats.PublishedMessages != 1 || stats.Subscribers != 2 {
		t.Fatalf("coordinator stats=%#v", stats)
	}
}

func TestMessageChangeCoordinatorTargetsChangedShardDespiteStaleSessionWatermark(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fixture := &messageChangeDBFixture{
		sessions:  []*model.Session{{UserName: "friend", NTime: now.Add(-time.Hour)}},
		messages:  map[string][]*model.Message{"friend": {}},
		callbacks: make(map[string][]func(fsnotify.Event) error),
	}
	coordinator := newMessageChangeCoordinator(fixture)
	if err := coordinator.Start(); err != nil {
		t.Fatal(err)
	}
	defer coordinator.Stop()
	received := make(chan ports.MessageChangeBatch, 1)
	cancel, err := coordinator.Subscribe("targeted", time.Now(), func(_ context.Context, batch ports.MessageChangeBatch) error {
		received <- batch
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	message := &model.Message{Talker: "friend", Time: now.Add(time.Second), DBLocalID: 9, Content: "new"}
	fixture.mu.Lock()
	fixture.messages["friend"] = append(fixture.messages["friend"], message)
	fixture.mu.Unlock()
	fixture.publishEvent("message", fsnotify.Event{Name: "/fixture/message/message_7.db-wal", Op: fsnotify.Write})

	select {
	case batch := <-received:
		if len(batch.Messages) != 1 || batch.Messages[0].DBLocalID != 9 {
			t.Fatalf("targeted batch=%#v", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale session watermark prevented targeted delivery")
	}
	fixture.mu.Lock()
	fullReads, targetReads := fixture.messageRead, fixture.targetRead
	fixture.mu.Unlock()
	if fullReads != 0 || targetReads != 1 {
		t.Fatalf("full reads=%d targeted reads=%d, want 0/1", fullReads, targetReads)
	}
	stats := coordinator.Stats()
	if stats.TargetedScans != 1 || stats.TargetedTalkers != 1 {
		t.Fatalf("targeted stats=%#v", stats)
	}
}

func TestMessageChangeWarningCoalescesTransientRetries(t *testing.T) {
	coordinator := newMessageChangeCoordinator(&messageChangeDBFixture{})
	defer coordinator.Stop()
	base := time.Now()
	first := errors.New("inspect shard message_0.db: file is not a database")
	second := errors.New("inspect shard message_1.db: file is not a database")
	if emit, suppressed := coordinator.shouldLogScanWarning(first, base); !emit || suppressed != 0 {
		t.Fatalf("first warning emit=%t suppressed=%d", emit, suppressed)
	}
	if emit, _ := coordinator.shouldLogScanWarning(second, base.Add(time.Second)); emit {
		t.Fatal("equivalent transient warning was not coalesced")
	}
	if coordinator.Stats().SuppressedWarnings != 1 {
		t.Fatalf("suppressed warning stats = %#v", coordinator.Stats())
	}
	if emit, suppressed := coordinator.shouldLogScanWarning(first, base.Add(messageChangeWarningWindow)); !emit || suppressed != 1 {
		t.Fatalf("window warning emit=%t suppressed=%d", emit, suppressed)
	}
	coordinator.clearScanWarning()
	if emit, suppressed := coordinator.shouldLogScanWarning(second, base.Add(messageChangeWarningWindow+time.Second)); !emit || suppressed != 0 {
		t.Fatalf("recovered warning emit=%t suppressed=%d", emit, suppressed)
	}
}
