package messagehook

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

type RuleMatch = ports.HookRuleMatch

func (s *Service) buildEventForRules(trigger *model.Message, rules []RuleMatch, triggerContent string, cfg *conf.MessageHook) Event {
	talker := trigger.Talker
	if strings.TrimSpace(talker) == "" {
		talker = trigger.TalkerName
	}
	talkerName := trigger.TalkerName
	if talkerName == "" {
		talkerName = talker
	}
	sender := trigger.Sender
	if sender == "" {
		sender = trigger.SenderName
	}
	senderName := trigger.SenderName
	if senderName == "" {
		senderName = sender
	}
	beforeCount := 5
	afterCount := 5
	if cfg != nil && cfg.BeforeCount >= 0 {
		beforeCount = cfg.BeforeCount
	}
	if cfg != nil && cfg.AfterCount >= 0 {
		afterCount = cfg.AfterCount
	}
	eventID, numericID := stableEventIdentity(stripChatlogLocalSuffix(s.conf.GetAccount()), talker, trigger.Seq)
	evt := Event{
		ID:               numericID,
		EventID:          eventID,
		CreatedAt:        time.Now().Format(time.RFC3339),
		MatchedRules:     append([]RuleMatch(nil), rules...),
		Talker:           talker,
		TalkerName:       talkerName,
		Sender:           sender,
		SenderName:       senderName,
		TriggerSeq:       trigger.Seq,
		TriggerType:      trigger.Type,
		TriggerSubType:   trigger.SubType,
		TriggerIsSelf:    trigger.IsSelf,
		TriggerContents:  cloneMessageContents(trigger.Contents),
		TriggerMediaURLs: s.triggerMediaURLs(trigger),
		TriggerTime:      trigger.Time.Format("2006-01-02 15:04:05"),
		TriggerContent:   triggerContent,
		OwnerWxid:        stripChatlogLocalSuffix(s.conf.GetAccount()),
		AtUserList:       trigger.AtUserList,
	}
	if len(rules) > 0 {
		evt.RuleType = rules[0].RuleType
		evt.RuleLabel = rules[0].RuleLabel
		evt.Keyword = rules[0].Keyword
	}
	evt.Context = s.loadContext(trigger, beforeCount, afterCount)
	return evt
}

func (s *Service) triggerMediaURLs(message *model.Message) []string {
	mediaType, keys := extractTriggerMediaRef(message)
	if mediaType == "" {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if value := s.buildTriggerMediaURL(mediaType, key); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cloneMessageContents(contents map[string]interface{}) map[string]interface{} {
	if len(contents) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(contents))
	for key, value := range contents {
		out[key] = value
	}
	return out
}

func stableEventIdentity(account, talker string, seq int64) (string, int64) {
	sum := sha256.Sum256([]byte(account + "\x00" + talker + "\x00" + fmt.Sprintf("%d", seq)))
	numeric := int64(binary.BigEndian.Uint64(sum[:8]) & ((1 << 63) - 1))
	return fmt.Sprintf("%x", sum[:]), numeric
}

func (s *Service) loadContext(trigger *model.Message, beforeCount, afterCount int) []ContextMessage {
	if beforeCount == 0 && afterCount == 0 {
		return nil
	}
	msgs, err := s.db.GetMessages(trigger.Time.Add(-24*time.Hour), trigger.Time.Add(24*time.Hour), trigger.Talker, "", "", maxContextScan, 0)
	if err != nil || len(msgs) == 0 {
		return nil
	}
	idx := -1
	for i, m := range msgs {
		if m != nil && m.Seq == trigger.Seq {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	start := idx - beforeCount
	if start < 0 {
		start = 0
	}
	end := idx + afterCount + 1
	if end > len(msgs) {
		end = len(msgs)
	}

	out := make([]ContextMessage, 0, end-start)
	for i := start; i < end; i++ {
		m := msgs[i]
		if m == nil {
			continue
		}
		content := strings.TrimSpace(m.PlainTextContent())
		if content == "" {
			content = strings.TrimSpace(m.Content)
		}
		position := "before"
		if i == idx {
			position = "trigger"
		} else if i > idx {
			position = "after"
		}
		sender := m.SenderName
		if sender == "" {
			sender = m.Sender
		}
		out = append(out, ContextMessage{
			Seq:      m.Seq,
			Time:     m.Time.Format("2006-01-02 15:04:05"),
			Sender:   sender,
			IsSelf:   m.IsSelf,
			Type:     m.Type,
			SubType:  m.SubType,
			Content:  content,
			Contents: cloneMessageContents(m.Contents),
			Position: position,
		})
	}
	return out
}
