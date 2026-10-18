package model

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sjzar/chatlog/internal/model/wxproto"
	"github.com/sjzar/chatlog/pkg/util/zstd"
	"google.golang.org/protobuf/proto"
)

// CREATE TABLE Msg_md5(talker)(
// local_id INTEGER PRIMARY KEY AUTOINCREMENT,
// server_id INTEGER,
// local_type INTEGER,
// sort_seq INTEGER,
// real_sender_id INTEGER,
// create_time INTEGER,
// status INTEGER,
// upload_status INTEGER,
// download_status INTEGER,
// server_seq INTEGER,
// origin_source INTEGER,
// source TEXT,
// message_content TEXT,
// compress_content TEXT,
// packed_info_data BLOB,
// WCDB_CT_message_content INTEGER DEFAULT NULL,
// WCDB_CT_source INTEGER DEFAULT NULL
// )
type MessageV4 struct {
	LocalID         int64  `json:"local_id"`         // 本地唯一 ID
	SortSeq         int64  `json:"sort_seq"`         // 消息序号，10位时间戳 + 3位序号
	ServerID        int64  `json:"server_id"`        // 消息 ID，用于关联 voice
	LocalType       int64  `json:"local_type"`       // 消息类型
	UserName        string `json:"user_name"`        // 发送人，通过 Join Name2Id 表获得
	CreateTime      int64  `json:"create_time"`      // 消息创建时间，10位时间戳
	MessageContent  []byte `json:"message_content"`  // 消息内容，文字聊天内容 或 zstd 压缩内容
	CompressContent []byte `json:"-"`                // 旧格式 <msg><atuserlist><item>...
	Source          []byte `json:"-"`                // WeChat 4.x <msgsource><atuserlist>...，可能 zstd 压缩
	PackedInfoData  []byte `json:"packed_info_data"` // 额外数据，类似 proto，格式与 v3 有差异
	Status          int64  `json:"status"`           // 消息状态；仅在缺少可用 sender 信息时作为方向兜底
}

func (m *MessageV4) Wrap(talker string) *Message {

	uniqueID := (m.CreateTime * 1000000) + m.LocalID
	_m := &Message{
		Seq:        uniqueID,
		ID:         uniqueID,
		DBLocalID:  m.LocalID,
		ServerID:   m.ServerID,
		Time:       time.Unix(m.CreateTime, 0),
		Talker:     talker,
		IsChatRoom: strings.HasSuffix(talker, "@chatroom"),
		Sender:     m.UserName,
		Type:       m.LocalType,
		Contents:   make(map[string]interface{}),
		Version:    WeChatV4,
	}

	// 私聊方向以实际 sender 为准，避免 status=2 的历史/同步状态把收到的
	// 消息误判为自己发送。群聊尚无稳定的当前账号字段，只能保留状态兜底。
	if !_m.IsChatRoom && strings.TrimSpace(m.UserName) != "" {
		_m.IsSelf = talker != m.UserName
	} else {
		_m.IsSelf = m.Status == 2
	}

	content := ""
	if bytes.HasPrefix(m.MessageContent, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		if b, err := zstd.Decompress(m.MessageContent); err == nil {
			content = string(b)
		}
	} else {
		content = string(m.MessageContent)
	}

	if _m.IsChatRoom {
		if sender, body, ok := splitChatRoomSenderPrefix(content, m.UserName); ok {
			_m.Sender = sender
			content = body
		}
	}

	if err := _m.ParseMediaInfo(content); err != nil {
		// Schema-compatible rows can still contain unknown or partially
		// written XML. Preserve the database payload in the API instead of
		// silently returning an empty content field.
		_m.Content = content
	}

	// 语音消息
	if _m.Type == 34 {
		_m.Contents["voice"] = fmt.Sprint(m.ServerID)
		_m.Contents["voice_local_id"] = fmt.Sprint(m.LocalID)
	}

	if len(m.PackedInfoData) != 0 {
		if packedInfo := ParsePackedInfo(m.PackedInfoData); packedInfo != nil {
			// FIXME 尝试解决 v4 版本 xml 数据无法匹配到 hardlink 记录的问题
			if _m.Type == 3 && packedInfo.Image != nil {
				_talkerMd5Bytes := md5.Sum([]byte(talker))
				talkerMd5 := hex.EncodeToString(_talkerMd5Bytes[:])
				_m.Contents["path"] = filepath.Join("msg", "attach", talkerMd5, _m.Time.Format("2006-01"), "Img", packedInfo.Image.Md5)
			}
			if _m.Type == 43 && packedInfo.Video != nil {
				_m.Contents["path"] = filepath.Join("msg", "video", _m.Time.Format("2006-01"), packedInfo.Video.Md5)
			}
		}
	}

	_m.RefreshProxyFields()
	if _m.IsChatRoom {
		// WeChat 4.x source is authoritative. Only fall back to the legacy
		// compress_content path when source is absent or cannot be parsed.
		_m.AtUserList = parseAtUserListFromSource(m.Source)
		if len(_m.AtUserList) == 0 {
			_m.AtUserList = parseAtUserListFromCompressContent(m.CompressContent)
		}
	}
	return _m
}

func splitChatRoomSenderPrefix(content, sender string) (string, string, bool) {
	// WeChat sometimes stores group payloads as "sender:\nbody". The sender is
	// also available through real_sender_id, so require an exact match before
	// stripping the prefix. Splitting on the first ":\n" unconditionally
	// corrupts ordinary text, system XML and quoted messages containing that
	// character sequence later in their payload.
	if strings.TrimSpace(sender) == "" {
		return "", content, false
	}
	prefix, body, ok := strings.Cut(content, ":\n")
	if !ok || prefix != sender {
		return "", content, false
	}
	return prefix, body, true
}

type atUserListEnvelope struct {
	AtUserList struct {
		Text  string   `xml:",chardata"`
		Items []string `xml:"item"`
	} `xml:"atuserlist"`
}

func parseAtUserListFromSource(src []byte) []string {
	return parseAtUserListXML(src)
}

func parseAtUserListFromCompressContent(content []byte) []string {
	return parseAtUserListXML(content)
}

func parseAtUserListXML(raw []byte) []string {
	payload, ok := decodeMessageMetadata(raw)
	if !ok {
		return nil
	}
	var envelope atUserListEnvelope
	if err := xml.Unmarshal(payload, &envelope); err != nil {
		return nil
	}
	values := envelope.AtUserList.Items
	if len(values) == 0 {
		values = strings.Split(envelope.AtUserList.Text, ",")
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func decodeMessageMetadata(raw []byte) ([]byte, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	payload := raw
	if bytes.HasPrefix(payload, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		decoded, err := zstd.Decompress(payload)
		if err != nil {
			return nil, false
		}
		payload = decoded
	}
	payload = bytes.TrimSpace(payload)
	start := bytes.IndexByte(payload, '<')
	end := bytes.LastIndexByte(payload, '>')
	if start < 0 || end < start {
		return nil, false
	}
	return payload[start : end+1], true
}

func ParsePackedInfo(b []byte) *wxproto.PackedInfo {
	var pbMsg wxproto.PackedInfo
	if err := proto.Unmarshal(b, &pbMsg); err != nil {
		return nil
	}
	return &pbMsg
}
