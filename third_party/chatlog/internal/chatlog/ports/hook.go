package ports

// HookContextMessage is the transport-neutral message snapshot attached to a
// hook event.
type HookContextMessage struct {
	Seq      int64                  `json:"seq"`
	Time     string                 `json:"time"`
	Sender   string                 `json:"sender"`
	IsSelf   bool                   `json:"is_self"`
	Type     int64                  `json:"type"`
	SubType  int64                  `json:"sub_type,omitempty"`
	Content  string                 `json:"content"`
	Contents map[string]interface{} `json:"contents,omitempty"`
	Position string                 `json:"position"`
}

type HookRuleMatch struct {
	RuleType  string `json:"rule_type"`
	RuleLabel string `json:"rule_label"`
	Keyword   string `json:"keyword,omitempty"`
}

type HookDeliveryResult struct {
	Target      string `json:"target"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Success     bool   `json:"success"`
	Attempts    int    `json:"attempts,omitempty"`
	NextRetryAt string `json:"next_retry_at,omitempty"`
}

// HookEvent is shared through an application port so HTTP and database
// orchestration do not depend on the message-hook implementation package.
type HookEvent struct {
	ID               int64                  `json:"id"`
	EventID          string                 `json:"event_id"`
	CreatedAt        string                 `json:"created_at"`
	RuleType         string                 `json:"rule_type"`
	RuleLabel        string                 `json:"rule_label"`
	Keyword          string                 `json:"keyword"`
	MatchedRules     []HookRuleMatch        `json:"matched_rules,omitempty"`
	Talker           string                 `json:"talker"`
	TalkerName       string                 `json:"talker_name"`
	Sender           string                 `json:"sender"`
	SenderName       string                 `json:"sender_name"`
	TriggerSeq       int64                  `json:"trigger_seq"`
	TriggerType      int64                  `json:"trigger_type"`
	TriggerSubType   int64                  `json:"trigger_sub_type,omitempty"`
	TriggerIsSelf    bool                   `json:"trigger_is_self,omitempty"`
	TriggerContents  map[string]interface{} `json:"trigger_contents,omitempty"`
	TriggerMediaURLs []string               `json:"trigger_media_urls,omitempty"`
	TriggerTime      string                 `json:"trigger_time"`
	TriggerContent   string                 `json:"trigger_content"`
	OwnerWxid        string                 `json:"owner_wxid,omitempty"`
	AtUserList       []string               `json:"at_user_list,omitempty"`
	Context          []HookContextMessage   `json:"context"`
	Deliveries       []HookDeliveryResult   `json:"deliveries,omitempty"`
}

type HookStoreStats struct {
	EventCount        int64  `json:"event_count"`
	PendingDeliveries int64  `json:"pending_deliveries"`
	FailedDeliveries  int64  `json:"failed_deliveries"`
	CursorCount       int64  `json:"cursor_count"`
	LastEventAt       string `json:"last_event_at"`
}

type HookRuntimeStats struct {
	HookStoreStats
	Running            bool   `json:"running"`
	LastScanAt         string `json:"last_scan_at"`
	LastScanDurationMS int64  `json:"last_scan_duration_ms"`
	LastScanError      string `json:"last_scan_error"`
	ScannedSessions    int    `json:"scanned_sessions"`
	ScannedMessages    int    `json:"scanned_messages"`
	MatchedMessages    int    `json:"matched_messages"`
}
