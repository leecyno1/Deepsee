package model

import (
	"encoding/xml"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/sjzar/chatlog/pkg/util"
)

var Debug = false

const (
	WeChatV4 = "wechatv4"
)

const (
	// MessageTypeText 文本
	MessageTypeText = 1

	// MessageTypeImage 图片
	MessageTypeImage = 3

	// MessageTypeVoice 语音
	MessageTypeVoice = 34

	// MessageTypeCard 名片
	MessageTypeCard = 42

	// MessageTypeVideo 视频
	MessageTypeVideo = 43

	// MessageTypeAnimation 动画表情
	MessageTypeAnimation = 47

	// MessageTypeLocation 位置
	MessageTypeLocation = 48

	// MessageTypeShare 分享
	MessageTypeShare = 49

	// MessageTypeVOIP 语音通话
	MessageTypeVOIP = 50

	// MessageTypeSystem 系统
	MessageTypeSystem = 10000

	// MessageTypeSystemNotification 系统级控制通知
	MessageTypeSystemNotification = 11000
)

const (
	// MessageSubTypeText 文本
	MessageSubTypeText = 1

	// MessageSubTypeLink 链接分享
	MessageSubTypeLink = 4

	// MessageSubTypeLink2 链接分享
	MessageSubTypeLink2 = 5

	// MessageSubTypeFile 文件
	MessageSubTypeFile = 6

	// MessageSubTypeGIF 动图
	MessageSubTypeGIF = 8

	// MessageSubTypeRealtimeLocation 实时位置共享
	MessageSubTypeRealtimeLocation = 17

	// MessageSubTypeMergeForward 合并转发
	MessageSubTypeMergeForward = 19

	// MessageSubTypeNote 笔记
	MessageSubTypeNote = 24

	// MessageSubTypeMiniProgram 小程序
	MessageSubTypeMiniProgram = 33

	// MessageSubTypeMiniProgram2 小程序
	MessageSubTypeMiniProgram2 = 36

	// MessageSubTypeChannel 视频号
	MessageSubTypeChannel = 51

	// MessageSubTypeQuote 引用
	MessageSubTypeQuote = 57

	// MessageSubTypePat 拍一拍
	MessageSubTypePat = 62

	// MessageSubTypeFileUploading 文件发送中的通知
	MessageSubTypeFileUploading = 74

	// MessageSubTypeChannelLive 视频号直播
	MessageSubTypeChannelLive = 63

	// MessageSubTypeChatRoomNotice 群公告
	MessageSubTypeChatRoomNotice = 87

	// MessageSubTypeMusic 音乐
	MessageSubTypeMusic = 92

	// MessageSubTypePay 转账
	MessageSubTypePay = 2000

	// MessageSubTypeRedEnvelope 红包
	MessageSubTypeRedEnvelope = 2001

	// MessageSubTypeRedEnvelopeCover 红包封面
	MessageSubTypeRedEnvelopeCover = 2003
)

type Message struct {
	Version    string                 `json:"-"`                          // 消息版本，内部判断
	Seq        int64                  `json:"seq"`                        // 唯一序列号 (timestamp * 1000000 + local_id)
	ID         int64                  `json:"id"`                         // 冗余 ID 字段，确保某些客户端能正确解析
	DBLocalID  int64                  `json:"dbLocalID,omitempty"`        // 数据库原始 local_id
	ServerID   int64                  `json:"server_id,string,omitempty"` // 微信服务端 ID，可与实时通道去重
	Time       time.Time              `json:"time"`                       // 消息创建时间，10位时间戳
	Talker     string                 `json:"talker"`                     // 聊天对象，微信 ID or 群 ID
	TalkerName string                 `json:"talkerName"`                 // 聊天对象名称
	IsChatRoom bool                   `json:"isChatRoom"`                 // 是否为群聊消息
	Sender     string                 `json:"sender"`                     // 发送人，微信 ID
	SenderName string                 `json:"senderName"`                 // 发送人名称
	IsSelf     bool                   `json:"isSelf"`                     // 是否为自己发送的消息
	Type       int64                  `json:"type"`                       // 消息类型
	SubType    int64                  `json:"subType"`                    // 消息子类型
	Content    string                 `json:"content"`                    // 消息内容，文字聊天内容
	Contents   map[string]interface{} `json:"contents,omitempty"`         // 消息内容，多媒体消息，采用更灵活的记录方式
	AtUserList []string               `json:"at_user_list,omitempty"`     // 群聊 @ 提及的 wxid 列表

	// Debug Info
	MediaMsg *MediaMsg `json:"mediaMsg,omitempty"` // 原始多媒体消息，XML 格式
	SysMsg   *SysMsg   `json:"sysMsg,omitempty"`   // 原始系统消息，XML 格式
}

// MessageCursor identifies a stable position inside one conversation. A
// timestamp alone is not sufficient because WeChat may write multiple rows in
// the same second.
type MessageCursor struct {
	Timestamp int64 `json:"timestamp"`
	LocalID   int64 `json:"local_id"`
}

func (c MessageCursor) Before(other MessageCursor) bool {
	return c.Timestamp < other.Timestamp ||
		(c.Timestamp == other.Timestamp && c.LocalID < other.LocalID)
}

func (c MessageCursor) BeforeMessage(message *Message) bool {
	if message == nil {
		return false
	}
	return c.Before(MessageCursor{Timestamp: message.Time.Unix(), LocalID: message.DBLocalID})
}

func (m *Message) ParseMediaInfo(data string) error {

	m.Type, m.SubType = util.SplitInt64ToTwoInt32(m.Type)

	if m.Type == 1 {
		m.Content = data
		return nil
	}

	if m.Type == MessageTypeSystem {
		m.Sender = "系统消息"
		m.SenderName = ""
		var sysMsg SysMsg
		if err := xml.Unmarshal([]byte(data), &sysMsg); err != nil {
			m.setSystemDisplayContent(data)
			return nil
		}
		if Debug {
			m.SysMsg = &sysMsg
		}
		m.Content = sysMsg.String()
		if strings.TrimSpace(m.Content) == "" && strings.TrimSpace(data) != "" {
			// Some WeChat 4.x type=10000 rows are plain text containing an
			// inline <a> tag instead of a <sysmsg> envelope. encoding/xml can
			// successfully ignore that unmatched shape, so keep the source
			// text rather than returning an empty API content field.
			m.Content = data
		}
		m.setSystemDisplayContent(m.Content)
		return nil
	}

	if m.Type == MessageTypeSystemNotification {
		m.Sender = "系统通知"
		m.SenderName = ""
		if m.Contents == nil {
			m.Contents = make(map[string]interface{})
		}
		m.Contents["notification_sub_type"] = m.SubType
		m.Content = strings.TrimSpace(data)
		if m.Content == "" {
			m.Content = "[系统通知]"
		}
		return nil
	}

	if m.Type == MessageTypeVOIP {
		var voip VoIPMsg
		if err := xml.Unmarshal([]byte(data), &voip); err != nil {
			return err
		}
		m.Content = strings.TrimSpace(voip.Bubble.Message)
		m.Contents["room_type"] = voip.Bubble.RoomType
		m.Contents["msg_type"] = voip.Bubble.MsgType
		m.Contents["duration"] = voip.Bubble.Duration
		m.Contents["business"] = voip.Bubble.Business
		return nil
	}

	var msg MediaMsg
	err := xml.Unmarshal([]byte(data), &msg)
	if err != nil {
		return err
	}

	if m.Contents == nil {
		m.Contents = make(map[string]interface{})
	}

	if Debug {
		m.MediaMsg = &msg
	}

	switch m.Type {
	case MessageTypeImage:
		m.Contents["md5"] = msg.Image.MD5
		// CDN metadata required to request mid/HD download via WeChat-native or
		// future direct CDN client. Absent when the payload is incomplete.
		if msg.Image.AesKey != "" {
			m.Contents["aeskey"] = msg.Image.AesKey
		}
		if msg.Image.CdnMidImgUrl != "" {
			m.Contents["cdnmidimgurl"] = msg.Image.CdnMidImgUrl
		}
		if msg.Image.CdnBigImgUrl != "" {
			m.Contents["cdnbigimgurl"] = msg.Image.CdnBigImgUrl
		}
		if msg.Image.CdnThumbUrl != "" {
			m.Contents["cdnthumburl"] = msg.Image.CdnThumbUrl
		}
		if msg.Image.CdnThumbAesKey != "" {
			m.Contents["cdnthumbaeskey"] = msg.Image.CdnThumbAesKey
		}
		if msg.Image.HdLength != "" {
			m.Contents["hdlength"] = msg.Image.HdLength
		}
		if msg.Image.Length != "" {
			m.Contents["length"] = msg.Image.Length
		}
		if msg.Image.OriginSourceMd5 != "" {
			m.Contents["originsourcemd5"] = msg.Image.OriginSourceMd5
		}
		if msg.Image.EncryVer != "" {
			m.Contents["encryver"] = msg.Image.EncryVer
		}
		if msg.Image.HevcMidSize != "" {
			m.Contents["hevc_mid_size"] = msg.Image.HevcMidSize
		}
	case MessageTypeCard:
		m.Contents["username"] = msg.UserName
		m.Contents["nickname"] = msg.NickName
		m.Contents["alias"] = msg.Alias
	case MessageTypeVideo:
		if msg.Video.Md5 != "" {
			m.Contents["md5"] = msg.Video.Md5
		}
		if msg.Video.RawMd5 != "" {
			m.Contents["rawmd5"] = msg.Video.RawMd5
		}
	case MessageTypeAnimation:
		m.Contents["cdnurl"] = msg.Emoji.CdnURL
	case MessageTypeLocation:
		m.Contents["x"] = msg.Location.X
		m.Contents["y"] = msg.Location.Y
		m.Contents["label"] = msg.Location.Label
		m.Contents["cityname"] = msg.Location.CityName
		m.Contents["poiname"] = msg.Location.PoiName
		m.Contents["poiid"] = msg.Location.PoiID
		m.Contents["scale"] = msg.Location.Scale
		m.Contents["maptype"] = msg.Location.MapType
		m.Contents["adcode"] = msg.Location.Adcode
		m.Contents["building_id"] = msg.Location.BuildingID
		m.Contents["floor_name"] = msg.Location.FloorName
	case MessageTypeShare:
		m.SubType = int64(msg.App.Type)
		switch m.SubType {
		case MessageSubTypeText, MessageSubTypeLink, MessageSubTypeLink2:
			// 链接
			m.Contents["title"] = msg.App.Title
			m.Contents["desc"] = msg.App.Des
			m.Contents["url"] = msg.App.URL
		case MessageSubTypeFile:
			// 文件
			m.Contents["title"] = msg.App.Title
			m.Contents["md5"] = msg.App.MD5
		case MessageSubTypeRealtimeLocation:
			m.Contents["title"] = msg.App.Title
			m.Content = strings.TrimSpace(msg.App.Title)
		case MessageSubTypeFileUploading:
			m.Contents["title"] = msg.App.Title
			if msg.App.AppAttach != nil {
				m.Contents["file_ext"] = msg.App.AppAttach.FileExt
				m.Contents["total_len"] = msg.App.AppAttach.TotalLen
			}
		case MessageSubTypeMergeForward, MessageSubTypeNote, MessageSubTypeChatRoomNotice:
			// 合并转发 & 笔记
			m.Contents["title"] = msg.App.Title
			m.Contents["desc"] = msg.App.Des
			if msg.App.RecordItem == nil {
				break
			}
			recordInfo := &RecordInfo{}
			err := xml.Unmarshal([]byte(msg.App.RecordItem.CDATA), recordInfo)
			if err != nil {
				return err
			}
			m.Contents["recordInfo"] = recordInfo
		case MessageSubTypeMiniProgram, MessageSubTypeMiniProgram2:
			// 小程序
			m.Contents["title"] = msg.App.SourceDisplayName
			m.Contents["url"] = msg.App.URL
		case MessageSubTypeChannel:
			// 视频号
			if msg.App.FinderFeed == nil {
				break
			}
			m.Contents["title"] = strings.TrimSpace(strings.ReplaceAll(msg.App.FinderFeed.Desc, "\n", " "))
			if len(msg.App.FinderFeed.MediaList.Media) > 0 {
				m.Contents["url"] = msg.App.FinderFeed.MediaList.Media[0].URL
			}
		case MessageSubTypeQuote:
			// 引用
			m.Content = msg.App.Title
			if msg.App.ReferMsg == nil {
				break
			}
			subMsg := &Message{
				Type:       int64(msg.App.ReferMsg.Type),
				Time:       time.Unix(msg.App.ReferMsg.CreateTime, 0),
				Sender:     msg.App.ReferMsg.ChatUsr,
				SenderName: msg.App.ReferMsg.DisplayName,
			}
			if subMsg.Sender == "" {
				subMsg.Sender = msg.App.ReferMsg.FromUsr
			}
			if err := subMsg.ParseMediaInfo(msg.App.ReferMsg.Content); err != nil {
				break
			}
			m.Contents["refer"] = subMsg
		case MessageSubTypePat:
			// 拍一拍
			if msg.App.PatMsg != nil {
				if len(msg.App.PatMsg.Records.Record) != 0 {
					m.Sender = msg.App.PatMsg.Records.Record[0].FromUser
					m.Content = msg.App.PatMsg.Records.Record[0].Templete
				}
			}
			if msg.App.PatInfo != nil {
				m.Content = msg.App.Title
			}
		case MessageSubTypeChannelLive:
			// 视频号直播
			if msg.App.FinderLive == nil {
				break
			}
			m.Contents["title"] = msg.App.FinderLive.Desc
		case MessageSubTypeMusic:
			// 音乐
			m.Contents["title"] = msg.App.Title
			m.Contents["desc"] = msg.App.Des
			m.Contents["url"] = msg.App.URL
		case MessageSubTypePay:
			// 微信转账
			if msg.App.WCPayInfo == nil {
				break
			}
			// 1 实时转账
			// 3 实时转账收钱回执
			// 4 转账退还回执
			// 5 非实时转账收钱回执
			// 7 非实时转账
			_type := ""
			switch msg.App.WCPayInfo.PaySubType {
			case 1, 7:
				_type = "发送 "
			case 3, 5:
				_type = "接收 "
			case 4:
				_type = "退还 "
			}
			payMemo := ""
			if len(msg.App.WCPayInfo.PayMemo) > 0 {
				payMemo = "(" + msg.App.WCPayInfo.PayMemo + ")"
			}
			m.Content = fmt.Sprintf("[转账|%s%s]%s", _type, msg.App.WCPayInfo.FeeDesc, payMemo)
		case MessageSubTypeRedEnvelope, MessageSubTypeRedEnvelopeCover:
			wcpay := msg.App.WCPayInfo
			if wcpay == nil {
				break
			}
			title := firstTrimmed(
				wcpay.ReceiverTitle,
				wcpay.SenderTitle,
				msg.App.Title,
			)
			status := strings.TrimSpace(wcpay.ReceiverDesc)
			if m.IsSelf {
				status = strings.TrimSpace(wcpay.SenderDesc)
			}
			if status == "" {
				status = strings.TrimSpace(msg.App.Des)
			}
			m.Contents["red_envelope_title"] = title
			m.Contents["red_envelope_status"] = status
			m.Contents["scene_text"] = firstTrimmed(wcpay.SceneText, msg.App.Title, "微信红包")
			m.Contents["description"] = strings.TrimSpace(msg.App.Des)
			m.Contents["pay_msg_id"] = strings.TrimSpace(wcpay.PayMsgID)
			m.Contents["invalid_time"] = strings.TrimSpace(wcpay.InvalidTime)
			m.Contents["template_id"] = strings.TrimSpace(wcpay.TemplateID)
		}
	}

	m.RefreshProxyFields()
	return nil
}

func (m *Message) SetContent(key string, value interface{}) {
	if m.Contents == nil {
		m.Contents = make(map[string]interface{})
	}
	m.Contents[key] = value
}

func (m *Message) PlainText(showChatRoom bool, timeFormat string, host string) string {

	if timeFormat == "" {
		timeFormat = "01-02 15:04:05"
	}

	m.SetContent("host", host)

	buf := strings.Builder{}

	sender := m.Sender
	if m.IsSelf {
		sender = "我"
	}
	if m.SenderName != "" {
		buf.WriteString(m.SenderName)
		buf.WriteString("(")
		buf.WriteString(sender)
		buf.WriteString(")")
	} else {
		buf.WriteString(sender)
	}
	buf.WriteString(" ")

	buf.WriteString(fmt.Sprintf("[%d] ", m.Seq))

	if m.IsChatRoom && showChatRoom {
		buf.WriteString("[")
		if m.TalkerName != "" {
			buf.WriteString(m.TalkerName)
			buf.WriteString("(")
			buf.WriteString(m.Talker)
			buf.WriteString(")")
		} else {
			buf.WriteString(m.Talker)
		}
		buf.WriteString("] ")
	}

	buf.WriteString(m.Time.Format(timeFormat))
	buf.WriteString("\n")

	buf.WriteString(m.PlainTextContent())
	buf.WriteString("\n")

	return buf.String()
}

func (m *Message) PlainTextContent() string {
	switch m.Type {
	case MessageTypeText:
		return m.Content
	case MessageTypeImage:
		if host, _ := m.Contents["host"].(string); host == "" {
			return "[图片]"
		}
		keylist := make([]string, 0)
		if m.Contents["md5"] != nil {
			if md5, ok := m.Contents["md5"].(string); ok {
				keylist = append(keylist, md5)
			}
		}
		if m.Contents["path"] != nil {
			if path, ok := m.Contents["path"].(string); ok {
				keylist = append(keylist, path)
			}
		}
		if m.Contents["thumbpath"] != nil {
			if thumbpath, ok := m.Contents["thumbpath"].(string); ok {
				keylist = append(keylist, thumbpath)
			}
		}
		return fmt.Sprintf("![图片](http://%s/image/%s)", m.Contents["host"], strings.Join(keylist, ","))
	case MessageTypeVoice:
		if host, _ := m.Contents["host"].(string); host == "" {
			return "[语音]"
		}
		if voice, ok := m.Contents["voice"]; ok {
			return fmt.Sprintf("[语音](http://%s/voice/%s)", m.Contents["host"], voice)
		}
		return "[语音]"
	case MessageTypeCard:
		name := strings.TrimSpace(fmt.Sprint(m.Contents["nickname"]))
		username := strings.TrimSpace(fmt.Sprint(m.Contents["username"]))
		if name != "" && username != "" {
			return fmt.Sprintf("[名片|%s](%s)", name, username)
		}
		if name != "" {
			return fmt.Sprintf("[名片|%s]", name)
		}
		return "[名片]"
	case MessageTypeVideo:
		if host, _ := m.Contents["host"].(string); host == "" {
			return "[视频]"
		}
		keylist := make([]string, 0)
		if m.Contents["md5"] != nil {
			if md5, ok := m.Contents["md5"].(string); ok {
				keylist = append(keylist, md5)
			}
		}
		if m.Contents["rawmd5"] != nil {
			if rawmd5, ok := m.Contents["rawmd5"].(string); ok {
				keylist = append(keylist, rawmd5)
			}
		}
		if m.Contents["path"] != nil {
			if path, ok := m.Contents["path"].(string); ok {
				keylist = append(keylist, path)
			}
		}
		return fmt.Sprintf("![视频](http://%s/video/%s)", m.Contents["host"], strings.Join(keylist, ","))
	case MessageTypeAnimation:
		if m.Contents["cdnurl"] != nil {
			if cdnURL, ok := m.Contents["cdnurl"].(string); ok {
				return fmt.Sprintf("![动画表情](%s)", cdnURL)
			}
		}
		return "[动画表情]"
	case MessageTypeLocation:
		keylist := make([]string, 0)
		for _, key := range []string{"poiname", "label", "cityname", "x", "y"} {
			if m.Contents[key] != nil {
				if value, ok := m.Contents[key].(string); ok {
					value = strings.TrimSpace(value)
					if value == "" || (key == "label" && value == "[位置]") {
						continue
					}
					keylist = append(keylist, value)
				}
			}
		}
		return fmt.Sprintf("[位置|%s]", strings.Join(keylist, "|"))
	case MessageTypeShare:
		switch m.SubType {
		case MessageSubTypeText:
			return fmt.Sprintf("[链接|%s](%s)", m.Contents["title"], m.Contents["desc"])
		case MessageSubTypeLink, MessageSubTypeLink2:
			return fmt.Sprintf("[链接|%s](%s)", m.Contents["title"], m.Contents["url"])
		case MessageSubTypeFile:
			if host, _ := m.Contents["host"].(string); host == "" {
				return fmt.Sprintf("[文件|%s]", m.Contents["title"])
			}
			return fmt.Sprintf("[文件|%s](http://%s/file/%s)", m.Contents["title"], m.Contents["host"], m.Contents["md5"])
		case MessageSubTypeGIF:
			return "[GIF表情]"
		case MessageSubTypeRealtimeLocation:
			if strings.TrimSpace(m.Content) != "" {
				return fmt.Sprintf("[实时位置共享|%s]", m.Content)
			}
			return "[实时位置共享]"
		case MessageSubTypeMergeForward:
			_recordInfo, ok := m.Contents["recordInfo"]
			if !ok {
				return "[合并转发]"
			}
			recordInfo, ok := _recordInfo.(*RecordInfo)
			if !ok {
				return "[合并转发]"
			}
			host := ""
			if m.Contents["host"] != nil {
				host = m.Contents["host"].(string)
			}
			return recordInfo.String("合并转发", "", host)
		case MessageSubTypeNote:
			_recordInfo, ok := m.Contents["recordInfo"]
			if !ok {
				return "[笔记]"
			}
			recordInfo, ok := _recordInfo.(*RecordInfo)
			if !ok {
				return "[笔记]"
			}
			host := ""
			if m.Contents["host"] != nil {
				host = m.Contents["host"].(string)
			}
			return recordInfo.String("笔记", "", host)
		case MessageSubTypeMiniProgram, MessageSubTypeMiniProgram2:
			if m.Contents["title"] == "" {
				return "[小程序]"
			}
			return fmt.Sprintf("[小程序|%s](%s)", m.Contents["title"], m.Contents["url"])
		case MessageSubTypeChannel:
			if m.Contents["title"] == "" {
				return "[视频号]"
			} else {
				return fmt.Sprintf("[视频号|%s](%s)", m.Contents["title"], m.Contents["url"])
			}
		case MessageSubTypeQuote:
			_refer, ok := m.Contents["refer"]
			if !ok {
				if m.Content == "" {
					return "[引用]"
				}
				return "> [引用]\n" + m.Content
			}
			refer, ok := _refer.(*Message)
			if !ok {
				if m.Content == "" {
					return "[引用]"
				}
				return "> [引用]\n" + m.Content
			}
			buf := strings.Builder{}
			host := ""
			if m.Contents["host"] != nil {
				host = m.Contents["host"].(string)
			}
			referContent := refer.PlainText(false, "", host)
			for _, line := range strings.Split(referContent, "\n") {
				if line == "" {
					continue
				}
				buf.WriteString("> ")
				buf.WriteString(line)
				buf.WriteString("\n")
			}
			buf.WriteString(m.Content)
			return buf.String()
		case MessageSubTypePat:
			return m.Content
		case MessageSubTypeFileUploading:
			if title := strings.TrimSpace(fmt.Sprint(m.Contents["title"])); title != "" {
				return fmt.Sprintf("[文件发送中|%s]", title)
			}
			return "[文件发送中]"
		case MessageSubTypeChannelLive:
			if m.Contents["title"] != nil {
				return fmt.Sprintf("[视频号直播|%s]", m.Contents["title"])
			}
			return "[视频号直播]"
		case MessageSubTypeChatRoomNotice:
			_recordInfo, ok := m.Contents["recordInfo"]
			if !ok {
				return "[群公告]"
			}
			recordInfo, ok := _recordInfo.(*RecordInfo)
			if !ok {
				return "[群公告]"
			}
			host := ""
			if m.Contents["host"] != nil {
				host = m.Contents["host"].(string)
			}
			return recordInfo.String("群公告", "", host)
		case MessageSubTypeMusic:
			return fmt.Sprintf("[音乐|%s](%s)", m.Contents["title"], m.Contents["url"])
		case MessageSubTypePay:
			return m.Content
		case MessageSubTypeRedEnvelope:
			return redEnvelopePlainText("红包", m.Contents)
		case MessageSubTypeRedEnvelopeCover:
			return redEnvelopePlainText("红包封面", m.Contents)
		default:
			return "[分享]"
		}
	case MessageTypeVOIP:
		if strings.TrimSpace(m.Content) != "" {
			return fmt.Sprintf("[语音通话|%s]", m.Content)
		}
		return "[语音通话]"
	case MessageTypeSystem, MessageTypeSystemNotification:
		return m.Content
	default:
		content := truncateText(m.Content, 120, "<...>")
		return fmt.Sprintf("Type: %d Content: %s", m.Type, content)
	}
}

var (
	systemImageTagPattern   = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	systemCustomLinkPattern = regexp.MustCompile(`(?is)<_wc_custom_link_\b[^>]*>(.*?)</_wc_custom_link_>`)
	systemMarkupPattern     = regexp.MustCompile(`(?is)</?[_a-z][^>]*>`)
	hongbaoSendIDPattern    = regexp.MustCompile(`(?i)(?:[?&]|&amp;)sendid=([0-9]+)`)
)

func (m *Message) setSystemDisplayContent(raw string) {
	raw = strings.TrimSpace(raw)
	if m.Contents == nil {
		m.Contents = make(map[string]interface{})
	}
	isHongbao := strings.Contains(raw, "SystemMessages_HongbaoIcon") ||
		strings.Contains(raw, "weixinhongbao") ||
		(strings.Contains(raw, "_wc_custom_link_") && strings.Contains(raw, "红包"))
	if !isHongbao {
		m.Content = raw
		return
	}

	if match := hongbaoSendIDPattern.FindStringSubmatch(raw); len(match) == 2 {
		m.Contents["hongbao_send_id"] = match[1]
	}
	cleaned := systemImageTagPattern.ReplaceAllString(raw, "")
	cleaned = systemCustomLinkPattern.ReplaceAllString(cleaned, "$1")
	cleaned = systemMarkupPattern.ReplaceAllString(cleaned, "")
	cleaned = html.UnescapeString(cleaned)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if cleaned == "" {
		cleaned = "红包状态更新"
	}
	m.Content = cleaned
	m.Contents["system_kind"] = "red_envelope_receipt"
	if strings.Contains(cleaned, "领取") {
		m.Contents["red_envelope_status"] = "已领取"
	}
}

func firstTrimmed(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func redEnvelopePlainText(kind string, contents map[string]interface{}) string {
	parts := []string{kind}
	for _, key := range []string{"red_envelope_title", "red_envelope_status"} {
		value := strings.TrimSpace(fmt.Sprint(contents[key]))
		if value != "" && value != "<nil>" {
			parts = append(parts, value)
		}
	}
	return "[" + strings.Join(parts, "|") + "]"
}

func truncateText(value string, limit int, suffix string) string {
	if limit < 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + suffix
}

func (m *Message) CSV(host string) []string {
	m.SetContent("host", host)
	return []string{
		fmt.Sprintf("%d", m.Seq),
		m.Time.Format("2006-01-02 15:04:05"),
		m.SenderName,
		m.Sender,
		m.TalkerName,
		m.Talker,
		m.PlainTextContent(),
	}
}
