package http

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
)

type databaseBusinessModule struct {
	ID           string                       `json:"id"`
	Label        string                       `json:"label"`
	Description  string                       `json:"description"`
	Status       string                       `json:"status"`
	Files        []databaseBusinessModuleFile `json:"files"`
	Capabilities []string                     `json:"capabilities"`
	Missing      []string                     `json:"missing,omitempty"`
}

type databaseBusinessModuleFile struct {
	Group string `json:"group"`
	File  string `json:"file"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

type databaseBusinessModuleRole struct {
	Name     string
	Required bool
	Match    func(group, path, base string) bool
}

type databaseBusinessModuleSpec struct {
	ID           string
	Label        string
	Description  string
	Capabilities []string
	Roles        []databaseBusinessModuleRole
}

func (s *Service) handleDatabaseModules(c *gin.Context) {
	dbs, err := s.db.GetDecryptedDBs()
	if err != nil {
		errors.Err(c, err)
		return
	}
	modules := buildDatabaseBusinessModules(dbs)
	ready, partial := 0, 0
	for _, module := range modules {
		switch module.Status {
		case "ready":
			ready++
		case "partial":
			partial++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"modules": modules,
		"summary": gin.H{
			"total":   len(modules),
			"ready":   ready,
			"partial": partial,
			"missing": len(modules) - ready - partial,
		},
	})
}

func buildDatabaseBusinessModules(dbs map[string][]string) []databaseBusinessModule {
	specs := databaseBusinessModuleSpecs()
	allFiles := make([]databaseBusinessModuleFile, 0)
	for group, files := range dbs {
		for _, file := range files {
			allFiles = append(allFiles, databaseBusinessModuleFile{
				Group: strings.ToLower(strings.TrimSpace(group)),
				File:  file,
				Name:  filepath.Base(file),
			})
		}
	}
	sort.Slice(allFiles, func(i, j int) bool {
		if allFiles[i].Group == allFiles[j].Group {
			return allFiles[i].File < allFiles[j].File
		}
		return allFiles[i].Group < allFiles[j].Group
	})

	out := make([]databaseBusinessModule, 0, len(specs))
	for _, spec := range specs {
		module := databaseBusinessModule{
			ID:           spec.ID,
			Label:        spec.Label,
			Description:  spec.Description,
			Status:       "missing",
			Files:        make([]databaseBusinessModuleFile, 0),
			Capabilities: append([]string(nil), spec.Capabilities...),
			Missing:      make([]string, 0),
		}
		matchedRequired := 0
		required := 0
		seenFiles := make(map[string]struct{})
		for _, role := range spec.Roles {
			if role.Required {
				required++
			}
			roleMatched := false
			for _, file := range allFiles {
				path := strings.ToLower(filepath.ToSlash(file.File))
				base := strings.ToLower(file.Name)
				if !role.Match(file.Group, path, base) {
					continue
				}
				roleMatched = true
				key := file.Group + "\x00" + file.File
				if _, exists := seenFiles[key]; exists {
					continue
				}
				seenFiles[key] = struct{}{}
				item := file
				item.Role = role.Name
				module.Files = append(module.Files, item)
			}
			if role.Required {
				if roleMatched {
					matchedRequired++
				} else {
					module.Missing = append(module.Missing, role.Name)
				}
			}
		}
		switch {
		case required == 0 && len(module.Files) > 0:
			module.Status = "ready"
		case required > 0 && matchedRequired == required:
			module.Status = "ready"
		case len(module.Files) > 0:
			module.Status = "partial"
		}
		out = append(out, module)
	}
	return out
}

func databaseBusinessModuleSpecs() []databaseBusinessModuleSpec {
	groupFile := func(group, base string) func(string, string, string) bool {
		return func(actualGroup, _, actualBase string) bool {
			return actualGroup == group && actualBase == base
		}
	}
	numberedShard := func(group, prefix string) func(string, string, string) bool {
		return func(actualGroup, _, base string) bool {
			if actualGroup != group || !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".db") {
				return false
			}
			number := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".db")
			if number == "" {
				return false
			}
			for _, character := range number {
				if character < '0' || character > '9' {
					return false
				}
			}
			return true
		}
	}
	pathContains := func(parts ...string) func(string, string, string) bool {
		return func(_, path, _ string) bool {
			for _, part := range parts {
				if strings.Contains(path, part) {
					return true
				}
			}
			return false
		}
	}
	return []databaseBusinessModuleSpec{
		{
			ID: "conversations", Label: "会话与消息",
			Description:  "最近会话、聊天记录、全文搜索、未读与统计。",
			Capabilities: []string{"sessions", "history", "message_search", "unread", "stats"},
			Roles: []databaseBusinessModuleRole{
				{Name: "会话索引", Required: true, Match: groupFile("session", "session.db")},
				{Name: "消息分片", Required: true, Match: numberedShard("message", "message_")},
				{Name: "消息搜索索引", Match: groupFile("message", "message_fts.db")},
			},
		},
		{
			ID: "address_book", Label: "联系人与群聊",
			Description:  "联系人、企业信息、群成员与显示名称解析。",
			Capabilities: []string{"contacts", "chatrooms", "members", "openim"},
			Roles: []databaseBusinessModuleRole{
				{Name: "联系人主库", Required: true, Match: groupFile("contact", "contact.db")},
				{Name: "联系人搜索索引", Match: groupFile("contact", "contact_fts.db")},
			},
		},
		{
			ID: "media_resources", Label: "媒体与资源",
			Description:  "图片、视频、语音、附件以及消息资源关联。",
			Capabilities: []string{"image", "video", "voice", "file", "message_resources"},
			Roles: []databaseBusinessModuleRole{
				{Name: "硬链接索引", Required: true, Match: groupFile("hardlink", "hardlink.db")},
				{Name: "语音分片", Match: numberedShard("message", "media_")},
				{Name: "消息资源库", Match: groupFile("message", "message_resource.db")},
			},
		},
		{
			ID: "social", Label: "朋友圈",
			Description:  "朋友圈动态、通知、内容搜索和媒体代理。",
			Capabilities: []string{"sns_feed", "sns_notifications", "sns_search", "sns_media"},
			Roles: []databaseBusinessModuleRole{
				{Name: "朋友圈主库", Required: true, Match: groupFile("sns", "sns.db")},
			},
		},
		{
			ID: "favorites", Label: "收藏",
			Description:  "收藏内容、类型筛选及收藏文本索引。",
			Capabilities: []string{"favorites", "favorite_search"},
			Roles: []databaseBusinessModuleRole{
				{Name: "收藏主库", Required: true, Match: groupFile("favorite", "favorite.db")},
				{Name: "收藏搜索索引", Match: groupFile("favorite", "favorite_fts.db")},
			},
		},
		{
			ID: "official_accounts", Label: "公众号与企业会话",
			Description:  "公众号消息分片、折叠会话与企业会话资料。",
			Capabilities: []string{"official_messages", "folded_sessions", "bizchat"},
			Roles: []databaseBusinessModuleRole{
				{Name: "公众号消息", Required: true, Match: numberedShard("message", "biz_message_")},
				{Name: "企业会话", Match: groupFile("bizchat", "bizchat.db")},
			},
		},
		{
			ID: "auxiliary", Label: "辅助业务数据",
			Description:  "头像、表情、接龙、通用配置、迁移与智能功能数据。",
			Capabilities: []string{"semantic_datasets", "dataset_search", "raw_browser", "read_only_sql", "export"},
			Roles: []databaseBusinessModuleRole{
				{Name: "辅助数据库", Match: pathContains("/head_image/", "/emoticon/", "/solitaire/", "/general/", "/migrate/", "/message/weclaw.db")},
			},
		},
	}
}
