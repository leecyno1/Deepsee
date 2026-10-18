package http

import (
	"encoding/xml"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
)

type favoriteXML struct {
	XMLName  xml.Name `xml:"favitem"`
	DataList struct {
		Items []struct {
			DataType  string `xml:"datatype,attr"`
			DataTitle string `xml:"datatitle"`
			DataDesc  string `xml:"datadesc"`
		} `xml:"dataitem"`
	} `xml:"datalist"`
}

func favoriteContentPreview(content string, favType int64) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	var item favoriteXML
	if err := xml.Unmarshal([]byte(content), &item); err != nil || item.XMLName.Local != "favitem" {
		return content
	}
	values := make([]string, 0, len(item.DataList.Items))
	seen := make(map[string]struct{}, len(item.DataList.Items))
	appendValue := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	// WeNote (type 18) stores the readable note body in datatype=1 while the
	// datatype=8 entry is only the backing HTML attachment.
	if favType == 18 {
		for _, data := range item.DataList.Items {
			if data.DataType == "1" {
				appendValue(data.DataDesc)
			}
		}
	}
	if len(values) == 0 {
		for _, data := range item.DataList.Items {
			appendValue(data.DataTitle)
			appendValue(data.DataDesc)
		}
	}
	if len(values) == 0 {
		return content
	}
	return strings.Join(values, "\n")
}

func (s *Service) initSocialRoutes(api *gin.RouterGroup) {
	api.GET("/favorites", s.handleFavorites)
	api.GET("/sns_notifications", s.handleSNSNotifications)
	api.GET("/sns_feed", s.handleSNSFeed)
	api.GET("/sns_search", s.handleSNSSearch)
	api.GET("/sns/media/proxy", s.handleSNSMediaProxy)
}

func (s *Service) handleFavorites(c *gin.Context) {
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 50, 1, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	favTypeValue, err := parseDatabaseQueryInteger(c.Query("fav_type"), 0, 0, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("fav_type"))
		return
	}
	favType := int64(favTypeValue)
	queryKw := strings.TrimSpace(c.Query("query"))

	file, err := s.findDBFile("favorite", "favorite.db")
	if err != nil {
		errors.Err(c, err)
		return
	}
	rows, err := s.db.ExecuteSQL("favorite", file, "SELECT * FROM fav_db_item ORDER BY rowid DESC")
	if err != nil {
		errors.Err(c, err)
		return
	}
	items := make([]gin.H, 0, min(limit, len(rows)))
	total := 0
	for _, r := range rows {
		ft := toInt64(r["type"])
		if favType != 0 && ft != favType {
			continue
		}
		content := toString(r["content"])
		preview := favoriteContentPreview(content, ft)
		if queryKw != "" && !strings.Contains(strings.ToLower(preview), strings.ToLower(queryKw)) &&
			!strings.Contains(strings.ToLower(content), strings.ToLower(queryKw)) {
			continue
		}
		total++
		if len(items) >= limit {
			continue
		}
		ts := toInt64(r["update_time"])
		if ts > 9_999_999_999 {
			ts /= 1000
		}
		typeName := map[int64]string{
			1: "文本", 2: "图片", 3: "语音", 4: "视频", 5: "文章", 6: "位置",
			8: "文件", 14: "聊天记录", 16: "笔记", 17: "小程序", 18: "笔记",
			19: "名片", 20: "视频",
		}[ft]
		if typeName == "" {
			typeName = "其他"
		}
		if len([]rune(preview)) > 100 {
			preview = string([]rune(preview)[:100]) + "..."
		}
		items = append(items, gin.H{
			"id":        toInt64(r["local_id"]),
			"type":      typeName,
			"type_num":  ft,
			"time":      time.Unix(ts, 0).Format("2006-01-02 15:04"),
			"timestamp": ts,
			"preview":   preview,
			"from":      toString(r["fromusr"]),
			"chat":      toString(r["realchatname"]),
		})
	}
	writeJSON(c, gin.H{"count": len(items), "total": total, "items": items})
}

func (s *Service) handleSNSNotifications(c *gin.Context) {
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 50, 1, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	start, end, hasRange, err := parseSinceUntil(c.Query("time"), c.Query("since"), c.Query("until"))
	if err != nil {
		errors.Err(c, err)
		return
	}
	includeReadValue, err := parseOptionalBool(c.DefaultQuery("include_read", "false"))
	if err != nil {
		errors.Err(c, errors.InvalidArg("include_read"))
		return
	}
	includeRead := includeReadValue != nil && *includeReadValue
	file, err := s.findDBFile("sns", "sns.db")
	if err != nil {
		errors.Err(c, err)
		return
	}
	rows, err := s.db.ExecuteSQL("sns", file, `SELECT local_id, create_time, type, feed_id, from_username, from_nickname, content, is_unread
FROM SnsMessage_tmp3 ORDER BY create_time DESC`)
	if err != nil {
		errors.Err(c, err)
		return
	}
	out := make([]gin.H, 0, min(limit, len(rows)))
	total := 0
	for _, r := range rows {
		if !includeRead && toInt64(r["is_unread"]) == 0 {
			continue
		}
		ts := toInt64(r["create_time"])
		tm := time.Unix(ts, 0)
		if hasRange {
			if !start.IsZero() && tm.Before(start) {
				continue
			}
			if !end.IsZero() && tm.After(end) {
				continue
			}
		}
		total++
		if len(out) >= limit {
			continue
		}
		content := toString(r["content"])
		kind := "comment"
		if strings.TrimSpace(content) == "" {
			kind = "like"
		}
		out = append(out, gin.H{
			"id":                   toInt64(r["local_id"]),
			"type":                 kind,
			"type_num":             toInt64(r["type"]),
			"time":                 tm.Format("01-02 15:04"),
			"timestamp":            ts,
			"from_username":        toString(r["from_username"]),
			"from_nickname":        toString(r["from_nickname"]),
			"content":              content,
			"feed_id":              toInt64(r["feed_id"]),
			"feed_author":          "",
			"feed_author_username": "",
			"feed_preview":         "",
		})
	}
	writeJSON(c, gin.H{"notifications": out, "total": total})
}

func extractXMLTagValue(xmlText, tag string) string {
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"
	start := strings.Index(xmlText, startTag)
	if start < 0 {
		return ""
	}
	start += len(startTag)
	end := strings.Index(xmlText[start:], endTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(xmlText[start : start+end])
}

func (s *Service) resolveSNSAuthor(c *gin.Context, username string) (displayName, avatarURL string) {
	displayName = username
	contact, _ := s.db.GetContact(username)
	if contact == nil {
		return displayName, ""
	}
	if resolved := strings.TrimSpace(contact.DisplayName()); resolved != "" {
		displayName = resolved
	}
	rawAvatar := strings.TrimSpace(contact.SmallHeadURL)
	if rawAvatar == "" {
		rawAvatar = strings.TrimSpace(contact.BigHeadURL)
	}
	if rawAvatar != "" {
		avatarURL = s.buildSNSMediaProxyURL(c, rawAvatar, "")
	}
	return displayName, avatarURL
}

func (s *Service) handleSNSFeed(c *gin.Context) {
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 20, 1, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	user := strings.TrimSpace(c.Query("user"))
	start, end, hasRange, err := parseSinceUntil(c.Query("time"), c.Query("since"), c.Query("until"))
	if err != nil {
		errors.Err(c, err)
		return
	}
	rows, err := s.db.GetSNSTimeline("", 0, 0)
	if err != nil {
		errors.Err(c, err)
		return
	}
	out := make([]gin.H, 0, min(limit, len(rows)))
	total := 0
	for _, r := range rows {
		tid := toInt64(r["tid"])
		content := toString(r["content"])
		post, parseErr := model.ParseSNSContent(content)
		if parseErr != nil || post == nil {
			post = &model.SNSPost{XMLContent: content}
		}
		author := toString(r["user_name"])
		if author == "" {
			author = extractXMLTagValue(content, "username")
		}
		desc := extractXMLTagValue(content, "contentDesc")
		if post.ContentDesc != "" {
			desc = post.ContentDesc
		}
		cts := toInt64(extractXMLTagValue(content, "createTime"))
		if post.CreateTime > 0 {
			cts = post.CreateTime
		}
		if cts == 0 {
			cts = tid / 1000000
		}
		tm := time.Unix(cts, 0)
		if hasRange {
			if !start.IsZero() && tm.Before(start) {
				continue
			}
			if !end.IsZero() && tm.After(end) {
				continue
			}
		}
		display, avatarURL := s.resolveSNSAuthor(c, author)
		if user != "" && !strings.Contains(strings.ToLower(author), strings.ToLower(user)) {
			if !strings.Contains(strings.ToLower(display), strings.ToLower(user)) {
				continue
			}
		}
		total++
		if len(out) >= limit {
			continue
		}
		out = append(out, gin.H{
			"id":           tid,
			"timestamp":    cts,
			"time":         tm.Format("2006-01-02 15:04"),
			"username":     author,
			"display":      display,
			"avatar_url":   avatarURL,
			"content":      desc,
			"content_raw":  post.ContentRaw,
			"raw_content":  content,
			"content_type": post.ContentType,
			"location":     post.Location,
			"media_list":   s.enrichSNSPostMedia(c, post),
			"article":      s.enrichSNSPostArticle(c, post),
			"finder_feed":  post.FinderFeed,
			"likes":        post.Likes,
			"comments":     post.Comments,
		})
	}
	writeJSON(c, gin.H{"count": len(out), "total": total, "items": out})
}

func snsPostSearchContent(post *model.SNSPost, description string) string {
	values := []string{description}
	if post == nil {
		return description
	}
	if post.Article != nil {
		values = append(values,
			post.Article.Title,
			post.Article.Description,
			post.Article.SourceName,
			post.Article.SourceUserName,
		)
	}
	if post.Location != nil {
		values = append(values,
			post.Location.City,
			post.Location.POIName,
			post.Location.POIAddress,
		)
	}
	for _, comment := range post.Comments {
		values = append(values,
			comment.NickName,
			comment.Content,
			comment.ReplyToNickName,
		)
	}
	return strings.Join(values, "\n")
}

func (s *Service) handleSNSSearch(c *gin.Context) {
	keyword := strings.TrimSpace(c.Query("keyword"))
	if keyword == "" {
		errors.Err(c, errors.InvalidArg("keyword"))
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 20, 1, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	user := strings.TrimSpace(c.Query("user"))
	start, end, hasRange, err := parseSinceUntil(c.Query("time"), c.Query("since"), c.Query("until"))
	if err != nil {
		errors.Err(c, err)
		return
	}
	rows, err := s.db.GetSNSTimeline("", 0, 0)
	if err != nil {
		errors.Err(c, err)
		return
	}
	out := make([]gin.H, 0, min(limit, len(rows)))
	total := 0
	for _, r := range rows {
		tid := toInt64(r["tid"])
		content := toString(r["content"])
		post, parseErr := model.ParseSNSContent(content)
		if parseErr != nil || post == nil {
			post = &model.SNSPost{XMLContent: content}
		}
		desc := extractXMLTagValue(content, "contentDesc")
		if post.ContentDesc != "" {
			desc = post.ContentDesc
		}
		if !strings.Contains(strings.ToLower(snsPostSearchContent(post, desc)), strings.ToLower(keyword)) {
			continue
		}
		author := toString(r["user_name"])
		if author == "" {
			author = extractXMLTagValue(content, "username")
		}
		cts := toInt64(extractXMLTagValue(content, "createTime"))
		if post.CreateTime > 0 {
			cts = post.CreateTime
		}
		if cts == 0 {
			cts = tid / 1000000
		}
		tm := time.Unix(cts, 0)
		if hasRange {
			if !start.IsZero() && tm.Before(start) {
				continue
			}
			if !end.IsZero() && tm.After(end) {
				continue
			}
		}
		display, avatarURL := s.resolveSNSAuthor(c, author)
		if user != "" && !strings.Contains(strings.ToLower(author), strings.ToLower(user)) &&
			!strings.Contains(strings.ToLower(display), strings.ToLower(user)) {
			continue
		}
		total++
		if len(out) >= limit {
			continue
		}
		out = append(out, gin.H{
			"id":           tid,
			"timestamp":    cts,
			"time":         tm.Format("2006-01-02 15:04"),
			"username":     author,
			"display":      display,
			"avatar_url":   avatarURL,
			"content":      desc,
			"content_raw":  post.ContentRaw,
			"raw_content":  content,
			"content_type": post.ContentType,
			"location":     post.Location,
			"media_list":   s.enrichSNSPostMedia(c, post),
			"article":      s.enrichSNSPostArticle(c, post),
			"finder_feed":  post.FinderFeed,
			"likes":        post.Likes,
			"comments":     post.Comments,
		})
	}
	writeJSON(c, gin.H{"count": len(out), "total": total, "items": out})
}
