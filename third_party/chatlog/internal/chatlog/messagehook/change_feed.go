package messagehook

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

// consumeMessageChangeBatch evaluates a page already read by the shared
// database coordinator. The hook's durable cursor remains authoritative, so
// replayed batches and startup overlap are idempotent.
func (s *Service) consumeMessageChangeBatch(ctx context.Context, batch ports.MessageChangeBatch) error {
	started := time.Now()
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	account := stripChatlogLocalSuffix(s.conf.GetAccount())
	if account == "" {
		account = "server"
	}
	cfg := s.conf.GetMessageHook()
	if !hookConfigActive(cfg) {
		state, exists, err := s.store.Activation(account)
		if err != nil {
			return err
		}
		if !exists || state.Enabled {
			if err := s.store.SetInactive(account, time.Now()); err != nil {
				return err
			}
		}
		return nil
	}
	activation, exists, err := s.store.Activation(account)
	if err != nil {
		return err
	}
	if !exists || !activation.Enabled || activation.RuleFingerprint != pushRuleFingerprint(cfg) {
		// Activation semantics intentionally begin after the rule change. Let the
		// ordinary one-shot activation scan snapshot all conversation cursors.
		s.Wake()
		return nil
	}
	talker := strings.TrimSpace(batch.Talker)
	if talker == "" || len(batch.Messages) == 0 {
		return nil
	}
	cursor, exists, err := s.store.Cursor(account, talker)
	if err != nil {
		return err
	}
	if !exists {
		cursor = model.MessageCursor{Timestamp: activation.EnabledAt, LocalID: math.MaxInt64}
		if err := s.store.Commit(account, talker, cursor, nil, nil); err != nil {
			return err
		}
	}
	messages := append([]*model.Message(nil), batch.Messages...)
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i] == nil || messages[j] == nil {
			return messages[i] != nil && messages[j] == nil
		}
		if messages[i].Time.Equal(messages[j].Time) {
			return messages[i].DBLocalID < messages[j].DBLocalID
		}
		return messages[i].Time.Before(messages[j].Time)
	})
	keywords := parseKeywords(cfg.Keywords)
	forwardContacts := parseTargetList(cfg.ForwardContacts)
	forwardChatRooms := parseTargetList(cfg.ForwardChatRooms)
	targets := deliveryTargets(cfg)
	scanned := 0
	matched := 0
	events := make([]Event, 0)
	finalCursor := cursor
	for _, message := range messages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if message == nil || !cursor.BeforeMessage(message) {
			continue
		}
		next := model.MessageCursor{Timestamp: message.Time.Unix(), LocalID: message.DBLocalID}
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
		cursor = next
		finalCursor = next
		scanned++
		if event != nil {
			events = append(events, *event)
		}
	}
	if scanned > 0 {
		if err := s.store.CommitEvents(account, talker, finalCursor, events, targets); err != nil {
			return err
		}
		if len(events) > 0 {
			s.wakeDeliveries()
		}
	}
	s.statsMu.Lock()
	s.lastScanAt = time.Now().Format(time.RFC3339)
	s.lastScanDuration = time.Since(started)
	s.lastScanError = ""
	s.scannedSessions = 1
	s.scannedMessages = scanned
	s.matchedMessages = matched
	s.statsMu.Unlock()
	return nil
}
