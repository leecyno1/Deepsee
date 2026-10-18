package wcdb

import (
	"context"
	"sort"
	"strings"

	"github.com/sjzar/chatlog/internal/model"
)

func (ds *DataSource) GetContacts(ctx context.Context, key string, limit, offset int) ([]*model.Contact, error) {
	_ = ctx
	// extra_buffer 必须读出来：ContactV4.Wrap 用它解析 @openim 用户的 corp_id。
	// 缺这一列，所有企业微信联系人 corp_id 都是空，下游也就拿不到企业名。
	rows, err := ds.client.Query("contact", "", `SELECT username, local_type, alias, remark, nick_name,
COALESCE(description, '') AS description,
COALESCE(big_head_url, '') AS big_head_url,
COALESCE(small_head_url, '') AS small_head_url,
extra_buffer FROM contact ORDER BY username`)
	if err != nil {
		return nil, err
	}
	items := make([]*model.Contact, 0, len(rows))
	for _, row := range rows {
		c := (&model.ContactV4{
			UserName:     toString(row["username"]),
			LocalType:    int(toInt64(row["local_type"])),
			Alias:        toString(row["alias"]),
			Remark:       toString(row["remark"]),
			NickName:     toString(row["nick_name"]),
			Description:  toString(row["description"]),
			BigHeadURL:   toString(row["big_head_url"]),
			SmallHeadURL: toString(row["small_head_url"]),
			ExtraBuffer:  toBytes(row["extra_buffer"]),
		}).Wrap()
		if key != "" && !(c.UserName == key || c.Alias == key || c.Remark == key || c.NickName == key) {
			continue
		}
		items = append(items, c)
	}
	if offset > len(items) {
		return []*model.Contact{}, nil
	}
	if offset > 0 {
		items = items[offset:]
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// GetOpenimWordings 读取 contact.db 的 openim_wording 表，返回 corp_id → 企业名映射。
func (ds *DataSource) GetOpenimWordings(ctx context.Context) ([]*model.OpenimWording, error) {
	_ = ctx
	rows, err := ds.client.Query("contact", "",
		`SELECT lang_id, app_id, wording_id, wording, COALESCE(pinyin,'') AS pinyin, COALESCE(quan_pin,'') AS quan_pin FROM openim_wording`)
	if err != nil {
		return nil, err
	}
	out := make([]*model.OpenimWording, 0, len(rows))
	for _, row := range rows {
		out = append(out, &model.OpenimWording{
			LangID:    int(toInt64(row["lang_id"])),
			AppID:     toString(row["app_id"]),
			WordingID: toString(row["wording_id"]),
			Wording:   toString(row["wording"]),
			Pinyin:    toString(row["pinyin"]),
			QuanPin:   toString(row["quan_pin"]),
		})
	}
	return out, nil
}

func (ds *DataSource) GetChatRooms(ctx context.Context, key string, limit, offset int) ([]*model.ChatRoom, error) {
	_ = ctx
	rows, err := ds.client.Query("contact", "", `SELECT username, owner, ext_buffer FROM chat_room ORDER BY username`)
	if err != nil {
		return nil, err
	}
	items := make([]*model.ChatRoom, 0, len(rows))
	seen := make(map[string]bool)
	for _, row := range rows {
		chat := (&model.ChatRoomV4{
			UserName:  toString(row["username"]),
			Owner:     toString(row["owner"]),
			ExtBuffer: toBytes(row["ext_buffer"]),
		}).Wrap()
		if key != "" && chat.Name != key {
			continue
		}
		seen[chat.Name] = true
		items = append(items, chat)
	}
	// contact 表的 chat_room 有时缺失只在消息里出现过的群（例如从未打开过详情的群）。
	// 用各 message DB 的 Name2Id 表补齐这些 @chatroom，避免群列表漏项。
	if msgDBs, err2 := ds.client.ListMessageDBs(); err2 == nil {
		for _, dbPath := range msgDBs {
			n2iRows, err3 := ds.client.Query("message", dbPath, `SELECT user_name FROM Name2Id WHERE user_name LIKE '%@chatroom'`)
			if err3 != nil {
				continue
			}
			for _, r := range n2iRows {
				name := toString(r["user_name"])
				if name == "" || seen[name] {
					continue
				}
				if key != "" && name != key {
					continue
				}
				seen[name] = true
				items = append(items, &model.ChatRoom{
					Name:             name,
					Users:            make([]model.ChatRoomUser, 0),
					User2DisplayName: make(map[string]string),
				})
			}
		}
	}
	if offset > len(items) {
		return []*model.ChatRoom{}, nil
	}
	if offset > 0 {
		items = items[offset:]
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (ds *DataSource) GetSessions(ctx context.Context, key string, limit, offset int) ([]*model.Session, error) {
	_ = ctx
	rows, err := ds.client.GetSessions()
	if err != nil {
		return nil, err
	}
	items := make([]*model.Session, 0, len(rows))
	for _, row := range rows {
		s := (&model.SessionV4{
			Username:              toString(row["username"]),
			UnreadCount:           toInt64(row["unread_count"]),
			Summary:               toString(row["summary"]),
			LastTimestamp:         toInt64(row["last_timestamp"]),
			LastMsgSender:         toString(row["last_msg_sender"]),
			LastSenderDisplayName: toString(row["last_sender_display_name"]),
			LastMsgType:           toInt64(row["last_msg_type"]),
			LastMsgSubType:        toInt64(row["last_msg_sub_type"]),
		}).Wrap()
		if key != "" && !strings.Contains(s.UserName, key) && !strings.Contains(s.NickName, key) && !strings.Contains(s.Content, key) {
			continue
		}
		items = append(items, s)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].NOrder > items[j].NOrder
	})
	if offset > len(items) {
		return []*model.Session{}, nil
	}
	if offset > 0 {
		items = items[offset:]
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
