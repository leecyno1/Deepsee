package conf

import "strings"

const (
	HookNotifyPost   = "post"
	HookNotifyWeixin = "weixin"
	HookNotifyQQ     = "qq"
	HookNotifyAll    = "all"

	HookKeywordModeText     = "text"
	HookKeywordModeImageOCR = "image_ocr"
	HookKeywordModeMixed    = "mixed"
)

type MessageHook struct {
	Keywords         string `mapstructure:"keywords" json:"keywords"`
	KeywordMode      string `mapstructure:"keyword_mode" json:"keyword_mode"`
	EffectiveAt      int64  `mapstructure:"effective_at" json:"effective_at,omitempty"`
	NotifyMode       string `mapstructure:"notify_mode" json:"notify_mode"`
	PostURL          string `mapstructure:"post_url" json:"post_url"`
	BeforeCount      int    `mapstructure:"before_count" json:"before_count"`
	AfterCount       int    `mapstructure:"after_count" json:"after_count"`
	ForwardAll       bool   `mapstructure:"forward_all" json:"forward_all"`
	ForwardContacts  string `mapstructure:"forward_contacts" json:"forward_contacts"`
	ForwardChatRooms string `mapstructure:"forward_chatrooms" json:"forward_chatrooms"`
}

func MessageHookRulesEqual(left, right MessageHook) bool {
	left = NormalizeMessageHook(left)
	right = NormalizeMessageHook(right)
	return left.Keywords == right.Keywords &&
		left.KeywordMode == right.KeywordMode &&
		left.ForwardAll == right.ForwardAll &&
		left.ForwardContacts == right.ForwardContacts &&
		left.ForwardChatRooms == right.ForwardChatRooms
}

func NormalizeMessageHook(input MessageHook) MessageHook {
	input.Keywords = strings.TrimSpace(input.Keywords)
	input.KeywordMode = CanonicalHookKeywordMode(input.KeywordMode)
	input.NotifyMode = CanonicalHookNotifyMode(input.NotifyMode)
	input.PostURL = strings.TrimSpace(input.PostURL)
	input.ForwardContacts = strings.TrimSpace(input.ForwardContacts)
	input.ForwardChatRooms = strings.TrimSpace(input.ForwardChatRooms)
	if input.BeforeCount < 0 {
		input.BeforeCount = 0
	}
	if input.AfterCount < 0 {
		input.AfterCount = 0
	}
	if input.ForwardAll {
		input.Keywords = ""
		input.ForwardContacts = ""
		input.ForwardChatRooms = ""
	}
	return input
}

func CanonicalHookKeywordMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case HookKeywordModeImageOCR, HookKeywordModeMixed:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return HookKeywordModeText
	}
}

func IsHookKeywordMode(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", HookKeywordModeText, HookKeywordModeImageOCR, HookKeywordModeMixed:
		return true
	default:
		return false
	}
}

func HookKeywordModeIncludesText(raw string) bool {
	mode := CanonicalHookKeywordMode(raw)
	return mode == HookKeywordModeText || mode == HookKeywordModeMixed
}

func HookKeywordModeIncludesImageOCR(raw string) bool {
	mode := CanonicalHookKeywordMode(raw)
	return mode == HookKeywordModeImageOCR || mode == HookKeywordModeMixed
}

type HookNotifyTargets struct {
	Post   bool
	Weixin bool
	QQ     bool
}

func (t HookNotifyTargets) HasAny() bool {
	return t.Post || t.Weixin || t.QQ
}

func ParseHookNotifyTargets(raw string) (HookNotifyTargets, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return HookNotifyTargets{Post: true}, true
	}

	var targets HookNotifyTargets
	for _, token := range splitHookNotifyMode(raw) {
		switch token {
		case HookNotifyPost:
			targets.Post = true
		case HookNotifyWeixin:
			targets.Weixin = true
		case HookNotifyQQ:
			targets.QQ = true
		case HookNotifyAll:
			targets.Post = true
			targets.Weixin = true
			targets.QQ = true
		default:
			return HookNotifyTargets{}, false
		}
	}
	return targets, targets.HasAny()
}

func CanonicalHookNotifyMode(raw string) string {
	targets, ok := ParseHookNotifyTargets(raw)
	if !ok {
		return HookNotifyPost
	}
	if targets.Post && targets.Weixin && targets.QQ {
		return HookNotifyAll
	}
	parts := make([]string, 0, 3)
	if targets.Post {
		parts = append(parts, HookNotifyPost)
	}
	if targets.Weixin {
		parts = append(parts, HookNotifyWeixin)
	}
	if targets.QQ {
		parts = append(parts, HookNotifyQQ)
	}
	if len(parts) == 0 {
		return HookNotifyPost
	}
	return strings.Join(parts, ",")
}

func splitHookNotifyMode(raw string) []string {
	raw = strings.NewReplacer("|", ",", ";", ",", "+", ",", " ", ",").Replace(raw)
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
