package ctx

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/wechat"
	"github.com/sjzar/chatlog/pkg/config"
	"github.com/sjzar/chatlog/pkg/filecopy"
	clog "github.com/sjzar/chatlog/pkg/log"
	"github.com/sjzar/chatlog/pkg/util"
)

const (
	DefaultHTTPAddr = "127.0.0.1:5030"
)

// Context is a context for a chatlog.
// It is used to store information about the chatlog.
type Context struct {
	conf *conf.TUIConfig
	cm   *config.Manager
	mu   sync.RWMutex

	History map[string]conf.ProcessConfig

	// 微信账号相关状态
	Account     string
	Platform    string
	Version     int
	FullVersion string
	DataDir     string
	DataKey     string
	DataUsage   string
	ImgKey      string

	// 工作目录相关状态
	WorkDir   string
	WorkUsage string

	// HTTP服务相关状态
	HTTPEnabled bool
	HTTPAddr    string

	LastSession          time.Time
	OCR                  conf.OCRConfig
	HookKeywords         string
	HookKeywordMode      string
	HookEffectiveAt      int64
	HookNotifyMode       string
	HookPostURL          string
	HookBeforeCount      int
	HookAfterCount       int
	HookForwardAll       bool
	HookForwardContacts  string
	HookForwardChatRooms string
	LogRetentionDays     int

	// 当前选中的微信实例
	Current *wechat.Account
	PID     int
	ExePath string
	Status  string

	// 所有可用的微信实例
	WeChatInstances []*wechat.Account
}

func New(configPath string) (*Context, error) {

	conf, tcm, err := conf.LoadTUIConfig(configPath)
	if err != nil {
		return nil, err
	}

	ctx := &Context{
		conf: conf,
		cm:   tcm,
	}

	ctx.loadConfig()

	return ctx, nil
}

func (c *Context) loadConfig() {
	c.History = c.conf.ParseHistory()
	c.SwitchHistory(c.conf.LastAccount)
	c.Refresh()
}

func (c *Context) SwitchHistory(account string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	previousDataDir := c.DataDir
	previousWorkDir := c.WorkDir
	c.Current = nil
	c.PID = 0
	c.ExePath = ""
	c.Status = ""
	history, ok := c.History[account]
	if ok {
		c.Account = history.Account
		c.Platform = history.Platform
		c.Version = history.Version
		c.FullVersion = history.FullVersion
		c.DataKey = history.DataKey
		c.ImgKey = history.ImgKey
		c.DataDir = history.DataDir
		c.WorkDir = history.WorkDir
		c.HTTPEnabled = history.HTTPEnabled
		c.HTTPAddr = history.HTTPAddr
		c.OCR = history.OCR
		c.HookKeywords = history.HookKeywords
		c.HookKeywordMode = history.HookKeywordMode
		c.HookEffectiveAt = history.HookEffectiveAt
		c.HookNotifyMode = history.HookNotifyMode
		c.HookPostURL = history.HookPostURL
		c.HookBeforeCount = history.HookBeforeCount
		c.HookAfterCount = history.HookAfterCount
		c.HookForwardAll = history.HookForwardAll
		c.HookForwardContacts = history.HookForwardContacts
		c.HookForwardChatRooms = history.HookForwardChatRooms
		c.LogRetentionDays = clog.NormalizeRetentionDays(history.LogRetentionDays)
	} else {
		c.Account = ""
		c.Platform = ""
		c.Version = 0
		c.FullVersion = ""
		c.DataKey = ""
		c.ImgKey = ""
		c.DataDir = ""
		c.WorkDir = ""
		c.HTTPEnabled = false
		c.HTTPAddr = ""
		c.OCR = conf.DefaultOCRConfig()
		c.HookKeywords = ""
		c.HookKeywordMode = conf.HookKeywordModeText
		c.HookEffectiveAt = 0
		c.HookNotifyMode = conf.HookNotifyPost
		c.HookPostURL = ""
		c.HookBeforeCount = 5
		c.HookAfterCount = 5
		c.HookForwardAll = false
		c.HookForwardContacts = ""
		c.HookForwardChatRooms = ""
		c.LogRetentionDays = clog.DefaultRetentionDays
	}
	clog.SetRetention(c.LogRetentionDays)
	if c.DataDir != previousDataDir {
		c.DataUsage = ""
	}
	if c.WorkDir != previousWorkDir {
		c.WorkUsage = ""
	}
}

func (c *Context) SwitchCurrent(info *wechat.Account) {
	if info == nil {
		return
	}
	c.mu.Lock()
	// Same account with a new WeChat PID (common after controlled restart) keeps
	// the in-memory preference and address instead of reloading an older History
	// snapshot.
	sameAccount := c.Account != "" && c.Account == info.Name
	c.mu.Unlock()
	if !sameAccount {
		c.SwitchHistory(info.Name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Current = info
	c.refreshLocked()
}

func (c *Context) CurrentAccount() *wechat.Account {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Current
}

func (c *Context) DefaultWorkDir(account string) string {
	return util.WorkDirAt(c.GetRuntimeDir(), account)
}

func (c *Context) SetLastSession(value time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LastSession = value
}

func (c *Context) Refresh() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked()
}

func (c *Context) refreshLocked() {
	if c.Current != nil {
		c.Account = c.Current.Name
		c.Platform = c.Current.Platform
		c.Version = c.Current.Version
		c.FullVersion = c.Current.FullVersion
		c.PID = int(c.Current.PID)
		c.ExePath = c.Current.ExePath
		c.Status = c.Current.Status
		// 更新密钥数据 - 总是从Current同步到Context
		// 仅在Current中的密钥为非空时，才更新Context，以避免覆盖已有的有效密钥
		if c.Current.Key != "" {
			c.DataKey = c.Current.Key
		}
		if c.Current.ImgKey != "" {
			c.ImgKey = c.Current.ImgKey
		}
		if c.Current.DataDir != c.DataDir {
			c.DataDir = c.Current.DataDir
			c.DataUsage = ""
		}

	}
	if c.DataUsage == "" && c.DataDir != "" {
		c.DataUsage = util.GetDirSize(c.DataDir)
	}
	if c.WorkUsage == "" && c.WorkDir != "" {
		workSize := util.GetDirSize(c.WorkDir)
		cacheDir := filecopy.GetCacheDir()
		cacheSize := util.GetDirSize(cacheDir)
		c.WorkUsage = fmt.Sprintf("%s (Cache: %s)", workSize, cacheSize)
	}
}

func (c *Context) GetDataDir() string {
	return c.DataDir
}

// GetAccount 返回当前激活的 chatlog account（含 local hex suffix），用于
// messagehook 把它 strip 后填到 Event.OwnerWxid，让 webhook 接收方能判定
// 消息属于哪个微信账号，避免切号期间数据错乱。
func (c *Context) GetAccount() string {
	return c.Account
}

func (c *Context) GetWorkDir() string {
	return c.WorkDir
}

func (c *Context) GetRuntimeDir() string {
	if c.conf != nil && strings.TrimSpace(c.conf.ConfigDir) != "" {
		return c.conf.ConfigDir
	}
	return util.AppRootDir()
}

func (c *Context) GetVersion() int {
	return c.Version
}

func (c *Context) GetDataKey() string {
	return c.DataKey
}

func (c *Context) GetHTTPAddr() string {
	if c.HTTPAddr == "" {
		c.HTTPAddr = DefaultHTTPAddr
	}
	return c.HTTPAddr
}

func (c *Context) GetMessageHook() *conf.MessageHook {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snapshot := conf.NormalizeMessageHook(conf.MessageHook{
		Keywords:         c.HookKeywords,
		KeywordMode:      c.HookKeywordMode,
		EffectiveAt:      c.HookEffectiveAt,
		NotifyMode:       c.HookNotifyMode,
		PostURL:          c.HookPostURL,
		BeforeCount:      c.HookBeforeCount,
		AfterCount:       c.HookAfterCount,
		ForwardAll:       c.HookForwardAll,
		ForwardContacts:  c.HookForwardContacts,
		ForwardChatRooms: c.HookForwardChatRooms,
	})
	return &snapshot
}

func (c *Context) UpdateMessageHook(next conf.MessageHook) error {
	next = conf.NormalizeMessageHook(next)
	c.mu.Lock()
	defer c.mu.Unlock()
	current := conf.MessageHook{
		Keywords: c.HookKeywords, KeywordMode: c.HookKeywordMode, EffectiveAt: c.HookEffectiveAt,
		ForwardAll: c.HookForwardAll, ForwardContacts: c.HookForwardContacts, ForwardChatRooms: c.HookForwardChatRooms,
	}
	if !conf.MessageHookRulesEqual(current, next) {
		if next.EffectiveAt <= 0 {
			next.EffectiveAt = time.Now().Unix()
		}
	} else {
		next.EffectiveAt = c.HookEffectiveAt
	}
	c.HookKeywords = next.Keywords
	c.HookKeywordMode = next.KeywordMode
	c.HookEffectiveAt = next.EffectiveAt
	c.HookNotifyMode = next.NotifyMode
	c.HookPostURL = next.PostURL
	c.HookBeforeCount = next.BeforeCount
	c.HookAfterCount = next.AfterCount
	c.HookForwardAll = next.ForwardAll
	c.HookForwardContacts = next.ForwardContacts
	c.HookForwardChatRooms = next.ForwardChatRooms
	c.UpdateConfig()
	return nil
}

func (c *Context) GetSaveDecryptedMedia() bool {
	// Default to true for now, can be made configurable later
	return true
}

func (c *Context) GetOCRConfig() conf.OCRConfig {
	c.mu.RLock()
	ocrConfig := c.OCR
	c.mu.RUnlock()
	return conf.OCRConfigWithEnv(ocrConfig)
}

func (c *Context) GetStoredOCRConfig() conf.OCRConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return conf.NormalizeOCRConfig(c.OCR)
}

func (c *Context) UpdateOCRConfig(next conf.OCRConfig) error {
	next = conf.NormalizeOCRConfig(next)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.OCR = next
	c.UpdateConfig()
	return nil
}

func (c *Context) SetHTTPEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HTTPEnabled == enabled {
		return
	}
	c.HTTPEnabled = enabled
	c.UpdateConfig()
}

func (c *Context) SetHTTPAddr(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HTTPAddr == addr {
		return
	}
	c.HTTPAddr = addr
	c.UpdateConfig()
}

func (c *Context) SetWorkDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.WorkDir == dir {
		return
	}
	c.WorkDir = dir
	c.WorkUsage = ""
	c.UpdateConfig()
	c.refreshLocked()
}

func (c *Context) SetDataDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.DataDir == dir {
		return
	}
	c.DataDir = dir
	c.DataUsage = ""
	c.UpdateConfig()
	c.refreshLocked()
}

func (c *Context) SetImgKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ImgKey == key {
		return
	}
	c.ImgKey = key
	c.UpdateConfig()
}

func (c *Context) SetDataKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.DataKey == key {
		return
	}
	c.DataKey = key
	c.UpdateConfig()
}

func (c *Context) SetHookKeywords(keywords string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HookKeywords == keywords {
		return
	}
	c.HookKeywords = keywords
	c.HookEffectiveAt = time.Now().Unix()
	c.UpdateConfig()
}

func (c *Context) SetHookNotifyMode(mode string) {
	mode = conf.CanonicalHookNotifyMode(mode)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HookNotifyMode == mode {
		return
	}
	c.HookNotifyMode = mode
	c.UpdateConfig()
}

func (c *Context) SetHookPostURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HookPostURL == url {
		return
	}
	c.HookPostURL = url
	c.UpdateConfig()
}

func (c *Context) SetHookBeforeCount(n int) {
	if n < 0 {
		n = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HookBeforeCount == n {
		return
	}
	c.HookBeforeCount = n
	c.UpdateConfig()
}

func (c *Context) SetLogRetentionDays(days int) {
	days = clog.NormalizeRetentionDays(days)
	c.mu.Lock()
	defer c.mu.Unlock()
	saved := c.History[c.Account].LogRetentionDays
	if c.LogRetentionDays == days && saved == days {
		return
	}
	c.LogRetentionDays = days
	clog.SetRetention(days)
	c.UpdateConfig()
}

func (c *Context) SetHookAfterCount(n int) {
	if n < 0 {
		n = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HookAfterCount == n {
		return
	}
	c.HookAfterCount = n
	c.UpdateConfig()
}

func (c *Context) SetHookForwardAll(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HookForwardAll == enabled {
		return
	}
	c.HookForwardAll = enabled
	c.HookEffectiveAt = time.Now().Unix()
	c.UpdateConfig()
}

func (c *Context) SetHookForwardContacts(raw string) {
	raw = strings.TrimSpace(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HookForwardContacts == raw {
		return
	}
	c.HookForwardContacts = raw
	c.HookEffectiveAt = time.Now().Unix()
	c.UpdateConfig()
}

func (c *Context) SetHookForwardChatRooms(raw string) {
	raw = strings.TrimSpace(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HookForwardChatRooms == raw {
		return
	}
	c.HookForwardChatRooms = raw
	c.HookEffectiveAt = time.Now().Unix()
	c.UpdateConfig()
}

// 更新配置
func (c *Context) UpdateConfig() {

	pconf := conf.ProcessConfig{
		Type:                 "wechat",
		Account:              c.Account,
		Platform:             c.Platform,
		Version:              c.Version,
		FullVersion:          c.FullVersion,
		DataDir:              c.DataDir,
		DataKey:              c.DataKey,
		ImgKey:               c.ImgKey,
		WorkDir:              c.WorkDir,
		HTTPEnabled:          c.HTTPEnabled,
		HTTPAddr:             c.HTTPAddr,
		OCR:                  c.OCR,
		HookKeywords:         c.HookKeywords,
		HookKeywordMode:      c.HookKeywordMode,
		HookEffectiveAt:      c.HookEffectiveAt,
		HookNotifyMode:       c.HookNotifyMode,
		HookPostURL:          c.HookPostURL,
		HookBeforeCount:      c.HookBeforeCount,
		HookAfterCount:       c.HookAfterCount,
		HookForwardAll:       c.HookForwardAll,
		HookForwardContacts:  c.HookForwardContacts,
		HookForwardChatRooms: c.HookForwardChatRooms,
		LogRetentionDays:     c.LogRetentionDays,
	}

	if c.conf.History == nil {
		c.conf.History = make([]conf.ProcessConfig, 0)
	}
	if len(c.conf.History) == 0 {
		c.conf.History = append(c.conf.History, pconf)
	} else {
		isFind := false
		for i, v := range c.conf.History {
			if v.Account == c.Account {
				isFind = true
				c.conf.History[i] = pconf
				break
			}
		}
		if !isFind {
			c.conf.History = append(c.conf.History, pconf)
		}
	}
	// Keep the in-memory History map aligned with conf.History so account
	// switching always restores the latest saved HTTP preference.
	if c.History == nil {
		c.History = make(map[string]conf.ProcessConfig)
	}
	if c.Account != "" {
		c.History[c.Account] = pconf
	}

	if err := c.cm.SetConfig("last_account", c.Account); err != nil {
		log.Error().Err(err).Msg("set last_account failed")
		return
	}

	if err := c.cm.SetConfig("history", c.conf.History); err != nil {
		log.Error().Err(err).Msg("set history failed")
		return
	}

}
