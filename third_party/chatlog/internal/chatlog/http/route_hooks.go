package http

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/hermespush"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
)

func (s *Service) initHookRoutes() {
	s.router.GET("/api/v1/hook/config", s.handleHookConfigGet)
	s.router.POST("/api/v1/hook/config", s.handleHookConfigSet)
	s.router.GET("/api/v1/hook/status", s.handleHookStatus)
	s.router.GET("/api/v1/hook/events", s.handleHookEvents)
	s.router.POST("/api/v1/hook/events/clear", s.handleHookEventsClear)
	s.router.POST("/api/v1/hook/events/batch-action", s.handleHookEventsBatchAction)
	s.router.POST("/api/v1/hook/events/:event_id/action", s.handleHookEventAction)
	s.router.GET("/api/v1/hook/hermes/weixin", s.handleHookHermesWeixinGet)
	s.router.POST("/api/v1/hook/hermes/weixin", s.handleHookHermesWeixinSet)
	s.router.GET("/api/v1/hook/hermes/qq", s.handleHookHermesQQGet)
	s.router.POST("/api/v1/hook/hermes/qq", s.handleHookHermesQQSet)
}

func (s *Service) handleHookConfigGet(c *gin.Context) {
	cfg := s.conf.GetMessageHook()
	if cfg == nil {
		cfg = &conf.MessageHook{}
	}
	writeJSON(c, gin.H{
		"keywords":          strings.TrimSpace(cfg.Keywords),
		"keyword_mode":      conf.CanonicalHookKeywordMode(cfg.KeywordMode),
		"effective_at":      cfg.EffectiveAt,
		"notify_mode":       conf.CanonicalHookNotifyMode(cfg.NotifyMode),
		"post_url":          strings.TrimSpace(cfg.PostURL),
		"before_count":      cfg.BeforeCount,
		"after_count":       cfg.AfterCount,
		"forward_all":       cfg.ForwardAll,
		"forward_contacts":  strings.TrimSpace(cfg.ForwardContacts),
		"forward_chatrooms": strings.TrimSpace(cfg.ForwardChatRooms),
	})
}

type hookConfigReq struct {
	Keywords         string `json:"keywords"`
	KeywordMode      string `json:"keyword_mode"`
	NotifyMode       string `json:"notify_mode"`
	PostURL          string `json:"post_url"`
	BeforeCount      int    `json:"before_count"`
	AfterCount       int    `json:"after_count"`
	ForwardAll       bool   `json:"forward_all"`
	ForwardContacts  string `json:"forward_contacts"`
	ForwardChatRooms string `json:"forward_chatrooms"`
}

func validateAbsoluteHTTPURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.InvalidArg("url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return nil
	default:
		return errors.InvalidArg("url")
	}
}

func (s *Service) handleHookConfigSet(c *gin.Context) {
	var req hookConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.Err(c, errors.InvalidArg("body"))
		return
	}

	if _, ok := conf.ParseHookNotifyTargets(req.NotifyMode); !ok {
		errors.Err(c, errors.InvalidArg("notify_mode"))
		return
	}
	if !conf.IsHookKeywordMode(req.KeywordMode) {
		errors.Err(c, errors.InvalidArg("keyword_mode"))
		return
	}
	if req.BeforeCount < 0 {
		errors.Err(c, errors.InvalidArg("before_count"))
		return
	}
	if req.AfterCount < 0 {
		errors.Err(c, errors.InvalidArg("after_count"))
		return
	}
	mode := conf.CanonicalHookNotifyMode(req.NotifyMode)
	targets, _ := conf.ParseHookNotifyTargets(mode)
	postURL := strings.TrimSpace(req.PostURL)
	if err := validateAbsoluteHTTPURL(postURL); err != nil {
		errors.Err(c, errors.InvalidArg("post_url"))
		return
	}
	keywords := strings.TrimSpace(req.Keywords)
	forwardContacts := strings.TrimSpace(req.ForwardContacts)
	forwardChatRooms := strings.TrimSpace(req.ForwardChatRooms)
	if req.ForwardAll {
		keywords = ""
		forwardContacts = ""
		forwardChatRooms = ""
	}
	hasForwardRule := req.ForwardAll || keywords != "" || forwardContacts != "" || forwardChatRooms != ""
	if targets.Post && hasForwardRule && postURL == "" {
		errors.Err(c, errors.InvalidArg("post_url"))
		return
	}
	if targets.Weixin {
		install := hermespush.DetectInstallation()
		if !install.Installed {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Hermes agent 未安装，无法启用 weixin 推送"})
			return
		}
		if _, err := hermespush.DiscoverWeixinConfig(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Hermes agent 未完成微信渠道配置: " + err.Error()})
			return
		}
	}
	if targets.QQ {
		install := hermespush.DetectInstallation()
		if !install.Installed {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Hermes agent 未安装，无法启用 qq 推送"})
			return
		}
		if _, err := hermespush.DiscoverQQConfig(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Hermes agent 未完成 QQ 渠道配置: " + err.Error()})
			return
		}
	}
	if err := s.conf.UpdateMessageHook(conf.MessageHook{
		Keywords:         keywords,
		KeywordMode:      conf.CanonicalHookKeywordMode(req.KeywordMode),
		NotifyMode:       mode,
		PostURL:          postURL,
		BeforeCount:      req.BeforeCount,
		AfterCount:       req.AfterCount,
		ForwardAll:       req.ForwardAll,
		ForwardContacts:  forwardContacts,
		ForwardChatRooms: forwardChatRooms,
	}); err != nil {
		errors.Err(c, err)
		return
	}
	if s.db != nil {
		s.db.WakeMessageHook()
	}
	s.handleHookConfigGet(c)
}

type hookHermesWeixinReq struct {
	HermesHome      string `json:"hermes_home"`
	AccountID       string `json:"account_id"`
	Token           string `json:"token"`
	BaseURL         string `json:"base_url"`
	CdnBaseURL      string `json:"cdn_base_url"`
	HomeChannel     string `json:"home_channel"`
	HomeChannelName string `json:"home_channel_name"`
}

type hookHermesQQReq struct {
	HermesHome      string `json:"hermes_home"`
	AppID           string `json:"app_id"`
	ClientSecret    string `json:"client_secret"`
	HomeChannel     string `json:"home_channel"`
	HomeChannelName string `json:"home_channel_name"`
}

func (s *Service) handleHookHermesWeixinGet(c *gin.Context) {
	mode := ""
	if cfg := s.conf.GetMessageHook(); cfg != nil {
		mode = conf.CanonicalHookNotifyMode(cfg.NotifyMode)
	}
	status := s.getHermesWeixinStatus(mode)
	c.JSON(http.StatusOK, status)
}

func (s *Service) handleHookHermesWeixinSet(c *gin.Context) {
	var req hookHermesWeixinReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.Err(c, errors.InvalidArg("body"))
		return
	}
	if err := validateAbsoluteHTTPURL(req.BaseURL); err != nil {
		errors.Err(c, errors.InvalidArg("base_url"))
		return
	}
	if err := validateAbsoluteHTTPURL(req.CdnBaseURL); err != nil {
		errors.Err(c, errors.InvalidArg("cdn_base_url"))
		return
	}
	install := hermespush.DetectInstallation()
	if !install.Installed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Hermes agent 未安装，无法保存微信渠道配置"})
		return
	}
	if _, err := hermespush.DiscoverWeixinConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前无法读取 Hermes Weixin 配置，已禁止编辑: " + err.Error()})
		return
	}
	cfg, err := hermespush.SaveWeixinConfig(hermespush.WeixinConfig{
		HermesHome:      strings.TrimSpace(req.HermesHome),
		AccountID:       strings.TrimSpace(req.AccountID),
		Token:           strings.TrimSpace(req.Token),
		BaseURL:         strings.TrimSpace(req.BaseURL),
		CdnBaseURL:      strings.TrimSpace(req.CdnBaseURL),
		HomeChannel:     strings.TrimSpace(req.HomeChannel),
		HomeChannelName: strings.TrimSpace(req.HomeChannelName),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	status := s.getHermesWeixinStatus("")
	status.Available = true
	status.Editable = cfg.EnvFile != "" || cfg.ConfigFile != "" || cfg.AccountFile != ""
	status.HermesHome = cfg.HermesHome
	status.EnvFile = cfg.EnvFile
	status.ConfigFile = cfg.ConfigFile
	status.ChannelFile = cfg.ChannelFile
	status.AccountFile = cfg.AccountFile
	status.AccountID = cfg.AccountID
	status.HasToken = cfg.Token != ""
	status.BaseURL = cfg.BaseURL
	status.CdnBaseURL = cfg.CdnBaseURL
	status.HomeChannel = cfg.HomeChannel
	status.HomeChannelName = cfg.HomeChannelName
	status.HomeChannelFrom = cfg.HomeChannelFrom
	c.JSON(http.StatusOK, status)
}

func (s *Service) handleHookHermesQQGet(c *gin.Context) {
	mode := ""
	if cfg := s.conf.GetMessageHook(); cfg != nil {
		mode = conf.CanonicalHookNotifyMode(cfg.NotifyMode)
	}
	status := s.getHermesQQStatus(mode)
	c.JSON(http.StatusOK, status)
}

func (s *Service) handleHookHermesQQSet(c *gin.Context) {
	var req hookHermesQQReq
	if err := c.ShouldBindJSON(&req); err != nil {
		errors.Err(c, errors.InvalidArg("body"))
		return
	}
	install := hermespush.DetectInstallation()
	if !install.Installed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Hermes agent 未安装，无法保存 QQ 渠道配置"})
		return
	}
	if _, err := hermespush.DiscoverQQConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前无法读取 Hermes QQ 配置，已禁止编辑: " + err.Error()})
		return
	}
	cfg, err := hermespush.SaveQQConfig(hermespush.QQConfig{
		HermesHome:      strings.TrimSpace(req.HermesHome),
		AppID:           strings.TrimSpace(req.AppID),
		ClientSecret:    strings.TrimSpace(req.ClientSecret),
		HomeChannel:     strings.TrimSpace(req.HomeChannel),
		HomeChannelName: strings.TrimSpace(req.HomeChannelName),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	status := s.getHermesQQStatus("")
	status.Available = true
	status.Editable = cfg.EnvFile != "" || cfg.ConfigFile != ""
	status.HermesHome = cfg.HermesHome
	status.EnvFile = cfg.EnvFile
	status.ConfigFile = cfg.ConfigFile
	status.AppID = cfg.AppID
	status.HasClientSecret = cfg.ClientSecret != ""
	status.HomeChannel = cfg.HomeChannel
	status.HomeChannelName = cfg.HomeChannelName
	c.JSON(http.StatusOK, status)
}

func splitHookKeywords(raw string) []string {
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

func (s *Service) handleHookStatus(c *gin.Context) {
	payload, err := s.hookStatusSnapshot()
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (s *Service) hookStatusSnapshot() (gin.H, error) {
	cfg := s.conf.GetMessageHook()
	var hookStats ports.HookRuntimeStats
	if s.db != nil {
		var statsErr error
		hookStats, statsErr = s.db.GetMessageHookStats()
		if statsErr != nil {
			return nil, statsErr
		}
	}
	running := hookStats.Running && s.db != nil && s.db.Ready()
	messageChanges := ports.MessageChangeStats{}
	if provider, ok := s.db.(MessageChangeStatusProvider); ok {
		messageChanges = provider.MessageChangeStats()
	}
	var keywords []string
	mode := ""
	postURL := ""
	before := 5
	after := 5
	keywordsRaw := ""
	keywordMode := conf.HookKeywordModeText
	effectiveAt := int64(0)
	forwardAll := false
	forwardContacts := ""
	forwardChatRooms := ""
	if cfg != nil {
		keywordsRaw = strings.TrimSpace(cfg.Keywords)
		keywordMode = conf.CanonicalHookKeywordMode(cfg.KeywordMode)
		effectiveAt = cfg.EffectiveAt
		keywords = splitHookKeywords(cfg.Keywords)
		mode = conf.CanonicalHookNotifyMode(cfg.NotifyMode)
		postURL = strings.TrimSpace(cfg.PostURL)
		if cfg.BeforeCount >= 0 {
			before = cfg.BeforeCount
		}
		if cfg.AfterCount >= 0 {
			after = cfg.AfterCount
		}
		forwardAll = cfg.ForwardAll
		forwardContacts = strings.TrimSpace(cfg.ForwardContacts)
		forwardChatRooms = strings.TrimSpace(cfg.ForwardChatRooms)
	}
	return gin.H{
		"running":               running,
		"keywords":              keywords,
		"keywords_raw":          keywordsRaw,
		"keyword_mode":          keywordMode,
		"effective_at":          effectiveAt,
		"keywords_count":        len(keywords),
		"keyword_separator":     "｜ or |",
		"target_separators":     []string{",", "，", ";", "|", "newline"},
		"ocr_keyword_enabled":   len(keywords) > 0 && conf.HookKeywordModeIncludesImageOCR(keywordMode),
		"notify_mode":           mode,
		"post_url":              postURL,
		"before_count":          before,
		"after_count":           after,
		"forward_all":           forwardAll,
		"forward_contacts":      splitHookTargets(forwardContacts),
		"forward_contacts_raw":  forwardContacts,
		"forward_chatrooms":     splitHookTargets(forwardChatRooms),
		"forward_chatrooms_raw": forwardChatRooms,
		"event_count":           hookStats.EventCount,
		"last_event_at":         hookStats.LastEventAt,
		"events_store_file":     s.db.MessageHookStorePath(),
		"pending_deliveries":    hookStats.PendingDeliveries,
		"failed_deliveries":     hookStats.FailedDeliveries,
		"cursor_count":          hookStats.CursorCount,
		"last_scan_at":          hookStats.LastScanAt,
		"last_scan_duration_ms": hookStats.LastScanDurationMS,
		"last_scan_error":       hookStats.LastScanError,
		"scanned_sessions":      hookStats.ScannedSessions,
		"scanned_messages":      hookStats.ScannedMessages,
		"matched_messages":      hookStats.MatchedMessages,
		"message_changes":       messageChanges,
		"weixin":                s.getHermesWeixinStatus(mode),
		"qq":                    s.getHermesQQStatus(mode),
	}, nil
}

func splitHookTargets(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	replacer := strings.NewReplacer("\n", ",", "，", ",", ";", ",", "|", ",")
	parts := strings.Split(replacer.Replace(raw), ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, part)
	}
	return out
}

func (s *Service) handleHookEvents(c *gin.Context) {
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 50, 1, 200)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	payload, err := s.hookEventsSnapshot(limit)
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (s *Service) hookEventsSnapshot(limit int) (gin.H, error) {
	if s.db == nil {
		return gin.H{"events": []ports.HookEvent{}}, nil
	}
	events, err := s.db.GetMessageHookEvents(limit)
	if err != nil {
		return nil, err
	}
	enrichHookEventProxyFields(events)
	return gin.H{"events": events}, nil
}

func enrichHookEventProxyFields(events []ports.HookEvent) {
	for eventIndex := range events {
		event := &events[eventIndex]
		trigger := &model.Message{
			Type:     event.TriggerType,
			SubType:  event.TriggerSubType,
			Contents: event.TriggerContents,
		}
		trigger.RefreshProxyFields()
		event.TriggerContents = trigger.Contents

		for contextIndex := range event.Context {
			contextMessage := &event.Context[contextIndex]
			message := &model.Message{
				Type:     contextMessage.Type,
				SubType:  contextMessage.SubType,
				Contents: contextMessage.Contents,
			}
			message.RefreshProxyFields()
			contextMessage.Contents = message.Contents
		}
	}
}

func (s *Service) handleHookEventsClear(c *gin.Context) {
	if s.db == nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "deleted": 0, "store_file": "", "event_count": 0})
		return
	}
	deleted, err := s.db.ClearMessageHookEvents()
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"deleted":     deleted,
		"store_file":  s.db.MessageHookStorePath(),
		"event_count": 0,
	})
}

type hookEventActionRequest struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

type hookEventBatchActionRequest struct {
	Action   string   `json:"action"`
	Target   string   `json:"target"`
	EventIDs []string `json:"event_ids"`
}

func (s *Service) handleHookEventsBatchAction(c *gin.Context) {
	var request hookEventBatchActionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		errors.Err(c, errors.InvalidArg("body"))
		return
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	request.Target = strings.TrimSpace(request.Target)
	seen := make(map[string]struct{}, len(request.EventIDs))
	eventIDs := make([]string, 0, len(request.EventIDs))
	for _, eventID := range request.EventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID == "" {
			continue
		}
		if _, exists := seen[eventID]; exists {
			continue
		}
		seen[eventID] = struct{}{}
		eventIDs = append(eventIDs, eventID)
	}
	if len(eventIDs) == 0 || len(eventIDs) > 200 {
		errors.Err(c, errors.InvalidArg("event_ids"))
		return
	}
	if request.Action != "retry" && request.Action != "delete" {
		errors.Err(c, errors.InvalidArg("action"))
		return
	}
	if s.db == nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "events": len(eventIDs), "action": request.Action, "affected": 0})
		return
	}
	admin, ok := s.db.(interface {
		RetryMessageHookEvents(eventIDs []string, target string) (int64, error)
		DeleteMessageHookEvents(eventIDs []string, target string) (int64, error)
	})
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "当前数据库适配器未提供批量投递操作"})
		return
	}
	var (
		affected int64
		err      error
	)
	if request.Action == "retry" {
		affected, err = admin.RetryMessageHookEvents(eventIDs, request.Target)
	} else {
		affected, err = admin.DeleteMessageHookEvents(eventIDs, request.Target)
	}
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "events": len(eventIDs), "action": request.Action,
		"target": request.Target, "affected": affected,
	})
}

func (s *Service) handleHookEventAction(c *gin.Context) {
	eventID := strings.TrimSpace(c.Param("event_id"))
	if eventID == "" {
		errors.Err(c, errors.InvalidArg("event_id"))
		return
	}
	var request hookEventActionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		errors.Err(c, errors.InvalidArg("body"))
		return
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	request.Target = strings.TrimSpace(request.Target)
	if request.Action != "cancel" && request.Action != "retry" && request.Action != "delete" {
		errors.Err(c, errors.InvalidArg("action"))
		return
	}
	if s.db == nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "event_id": eventID, "action": request.Action, "affected": 0})
		return
	}
	var (
		affected int64
		err      error
	)
	if request.Action == "cancel" {
		affected, err = s.db.CancelMessageHookEvent(eventID, request.Target)
	} else if request.Action == "delete" {
		deleter, ok := s.db.(interface {
			DeleteMessageHookEvent(eventID, target string) (int64, error)
		})
		if !ok {
			c.JSON(http.StatusNotImplemented, gin.H{"error": "当前数据库适配器未提供队列删除能力"})
			return
		}
		affected, err = deleter.DeleteMessageHookEvent(eventID, request.Target)
	} else {
		affected, err = s.db.RetryMessageHookEvent(eventID, request.Target)
	}
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "event_id": eventID, "action": request.Action,
		"target": request.Target, "affected": affected,
	})
}

// NoRoute handles 404 Not Found errors. If the request URL starts with "/api"
// or "/static", it responds with a JSON error. Otherwise, it redirects to the root path.
