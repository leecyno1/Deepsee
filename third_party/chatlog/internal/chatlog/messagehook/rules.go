package messagehook

import (
	"strings"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/model"
)

func (s *Service) matchRules(m *model.Message, content string, keywords []string, keywordMode string, forwardContacts, forwardChatRooms map[string]struct{}, forwardAll bool) []RuleMatch {
	out := make([]RuleMatch, 0, 4)
	if conf.HookKeywordModeIncludesText(keywordMode) && m.Type == model.MessageTypeText {
		if kw := matchKeyword(content, keywords); kw != "" {
			out = append(out, RuleMatch{RuleType: "keyword", RuleLabel: kw, Keyword: kw})
		}
	}
	talker := strings.TrimSpace(m.Talker)
	talkerName := strings.TrimSpace(m.TalkerName)
	isChatRoom := strings.HasSuffix(talker, "@chatroom")
	if forwardAll {
		out = append(out, RuleMatch{RuleType: "forward_all", RuleLabel: "all"})
	}
	if isChatRoom {
		if targetListContains(forwardChatRooms, talker, talkerName) {
			out = append(out, RuleMatch{RuleType: "forward_chatroom", RuleLabel: fallbackText(talkerName, talker)})
		}
	} else {
		if targetListContains(forwardContacts, talker, talkerName) {
			out = append(out, RuleMatch{RuleType: "forward_contact", RuleLabel: fallbackText(talkerName, talker)})
		}
	}
	return dedupeRules(out)
}

func parseTargetList(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	replacer := strings.NewReplacer("\n", ",", "，", ",", ";", ",", "|", ",")
	parts := strings.Split(replacer.Replace(raw), ",")
	out := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out[strings.ToLower(part)] = struct{}{}
	}
	return out
}

func targetListContains(targets map[string]struct{}, values ...string) bool {
	if len(targets) == 0 {
		return false
	}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := targets[value]; ok {
			return true
		}
	}
	return false
}

func dedupeRules(in []RuleMatch) []RuleMatch {
	seen := map[string]struct{}{}
	out := make([]RuleMatch, 0, len(in))
	for _, item := range in {
		key := item.RuleType + "\x00" + item.RuleLabel + "\x00" + item.Keyword
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func buildWeixinMessage(evt Event) string {
	lines := []string{
		"【chatlog 关键词命中】",
		"关键词: " + fallbackText(evt.Keyword),
		"会话: " + fallbackText(evt.TalkerName, evt.Talker),
		"发送者: " + fallbackText(evt.SenderName, evt.Sender),
		"时间: " + fallbackText(evt.TriggerTime),
		"内容: " + singleLine(evt.TriggerContent),
	}
	ctx := summarizeWeixinContext(evt.Context, 6)
	if ctx != "" {
		lines = append(lines, "上下文:", ctx)
	}
	msg := strings.Join(lines, "\n")
	runes := []rune(msg)
	if len(runes) > 3500 {
		return string(runes[:3500]) + "\n...(已截断)"
	}
	return msg
}

func summarizeWeixinContext(items []ContextMessage, limit int) string {
	if len(items) == 0 || limit <= 0 {
		return ""
	}
	if len(items) > limit {
		items = items[:limit]
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		tag := "上下文"
		switch strings.ToLower(strings.TrimSpace(item.Position)) {
		case "before":
			tag = "前文"
		case "trigger":
			tag = "命中"
		case "after":
			tag = "后文"
		}
		lines = append(lines, " - ["+tag+"] "+strings.TrimSpace(item.Time)+" "+strings.TrimSpace(item.Sender)+": "+singleLine(item.Content))
	}
	return strings.Join(lines, "\n")
}

func fallbackText(values ...string) string {
	for _, item := range values {
		item = strings.TrimSpace(item)
		if item != "" {
			return item
		}
	}
	return "-"
}

func singleLine(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.Join(strings.Fields(text), " ")
}

func parseKeywords(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	raw = strings.ReplaceAll(raw, "|", "｜")
	parts := strings.Split(raw, "｜")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		k := strings.TrimSpace(p)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func matchKeyword(content string, keywords []string) string {
	for _, k := range keywords {
		if strings.Contains(content, k) {
			return k
		}
	}
	return ""
}
