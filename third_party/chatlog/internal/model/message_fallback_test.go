package model

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMessageV4PreservesMalformedMediaPayload(t *testing.T) {
	const raw = "<msg><broken>"
	msg := (&MessageV4{
		LocalType:      MessageTypeImage,
		MessageContent: []byte(raw),
	}).Wrap("friend")
	if msg.Content != raw {
		t.Fatalf("content = %q, want raw database payload", msg.Content)
	}
}

func TestPlainTextContentTruncatesOnRuneBoundary(t *testing.T) {
	msg := &Message{Type: 999, Content: strings.Repeat("界", 121)}
	got := msg.PlainTextContent()
	if !utf8.ValidString(got) {
		t.Fatalf("PlainTextContent returned invalid UTF-8: %q", got)
	}
}

func TestMessageV4OnlyStripsVerifiedChatRoomSenderPrefix(t *testing.T) {
	t.Run("matching database sender", func(t *testing.T) {
		msg := (&MessageV4{
			LocalType:      MessageTypeText,
			UserName:       "wxid_sender",
			MessageContent: []byte("wxid_sender:\nhello"),
		}).Wrap("room@chatroom")
		if msg.Sender != "wxid_sender" || msg.Content != "hello" {
			t.Fatalf("message = sender %q content %q", msg.Sender, msg.Content)
		}
	})

	t.Run("delimiter inside body", func(t *testing.T) {
		const raw = "first line:\nsecond line"
		msg := (&MessageV4{
			LocalType:      MessageTypeText,
			UserName:       "wxid_sender",
			MessageContent: []byte(raw),
		}).Wrap("room@chatroom")
		if msg.Sender != "wxid_sender" || msg.Content != raw {
			t.Fatalf("message = sender %q content %q, want preserved body", msg.Sender, msg.Content)
		}
	})

	t.Run("missing database sender", func(t *testing.T) {
		const raw = "looks_like_sender:\nbody"
		msg := (&MessageV4{
			LocalType:      MessageTypeText,
			MessageContent: []byte(raw),
		}).Wrap("room@chatroom")
		if msg.Content != raw {
			t.Fatalf("content = %q, want %q", msg.Content, raw)
		}
	})
}

func TestMessageV4ParsesObservedSpecialFormats(t *testing.T) {
	t.Run("poi location", func(t *testing.T) {
		msg := (&MessageV4{
			LocalType: MessageTypeLocation,
			UserName:  "friend",
			MessageContent: []byte(
				`<msg><location x="28.141262" y="116.870050" scale="15" label="[位置]" maptype="roadmap" ` +
					`poiname="戴家西79米" poiid="nearby_fixture" buildingId="building" floorName="2F" cityname=""/></msg>`,
			),
		}).Wrap("friend")
		if got := msg.PlainTextContent(); got != "[位置|戴家西79米|28.141262|116.870050]" {
			t.Fatalf("PlainTextContent = %q", got)
		}
		if msg.Contents["poiname"] != "戴家西79米" ||
			msg.Contents["poiid"] != "nearby_fixture" ||
			msg.Contents["building_id"] != "building" ||
			msg.Contents["floor_name"] != "2F" {
			t.Fatalf("contents = %#v", msg.Contents)
		}
	})

	t.Run("red envelope", func(t *testing.T) {
		msg := (&MessageV4{
			LocalType: int64(MessageSubTypeRedEnvelope)<<32 | MessageTypeShare,
			UserName:  "friend",
			MessageContent: []byte(
				`<msg><appmsg><title>微信红包</title><des>我给你发了一个红包，赶紧去拆!</des><type>2001</type>` +
					`<wcpayinfo><receivertitle>恭喜发财，大吉大利</receivertitle><sendertitle>恭喜发财，大吉大利</sendertitle>` +
					`<receiverdes>领取红包</receiverdes><senderdes>查看红包</senderdes><scenetext>微信红包</scenetext>` +
					`<paymsgid>send-id</paymsgid><invalidtime>1784289302</invalidtime></wcpayinfo></appmsg></msg>`,
			),
		}).Wrap("friend")
		if got := msg.PlainTextContent(); got != "[红包|恭喜发财，大吉大利|领取红包]" {
			t.Fatalf("PlainTextContent = %q", got)
		}
		if msg.Contents["red_envelope_title"] != "恭喜发财，大吉大利" ||
			msg.Contents["red_envelope_status"] != "领取红包" ||
			msg.Contents["pay_msg_id"] != "send-id" {
			t.Fatalf("contents = %#v", msg.Contents)
		}
	})

	t.Run("realtime location", func(t *testing.T) {
		msg := (&MessageV4{
			LocalType:      int64(MessageSubTypeRealtimeLocation)<<32 | MessageTypeShare,
			MessageContent: []byte(`<msg><appmsg><title>位置共享</title><type>17</type></appmsg></msg>`),
		}).Wrap("friend")
		if got := msg.PlainTextContent(); got != "[实时位置共享|位置共享]" {
			t.Fatalf("PlainTextContent = %q", got)
		}
	})

	t.Run("uploading file", func(t *testing.T) {
		msg := (&MessageV4{
			LocalType: int64(MessageSubTypeFileUploading)<<32 | MessageTypeShare,
			MessageContent: []byte(
				`<msg><appmsg><title>report.xlsx</title><type>74</type><appattach><totallen>42</totallen><fileext>xlsx</fileext></appattach></appmsg></msg>`,
			),
		}).Wrap("friend")
		if got := msg.PlainTextContent(); got != "[文件发送中|report.xlsx]" {
			t.Fatalf("PlainTextContent = %q", got)
		}
		if msg.Contents["file_ext"] != "xlsx" || msg.Contents["total_len"] != "42" {
			t.Fatalf("contents = %#v", msg.Contents)
		}
	})

	t.Run("contact card", func(t *testing.T) {
		msg := (&MessageV4{
			LocalType:      MessageTypeCard,
			MessageContent: []byte(`<msg username="wxid_card" nickname="Card Name" alias="card_alias"/>`),
		}).Wrap("friend")
		if got := msg.PlainTextContent(); got != "[名片|Card Name](wxid_card)" {
			t.Fatalf("PlainTextContent = %q", got)
		}
	})

	t.Run("voip bubble", func(t *testing.T) {
		msg := (&MessageV4{
			LocalType: MessageTypeVOIP,
			MessageContent: []byte(
				`<voipmsg type="2"><VoIPBubbleMsg><msg>通话已结束</msg><room_type>1</room_type><msg_type>4</msg_type><duration>12</duration><business>0</business></VoIPBubbleMsg></voipmsg>`,
			),
		}).Wrap("friend")
		if got := msg.PlainTextContent(); got != "[语音通话|通话已结束]" {
			t.Fatalf("PlainTextContent = %q", got)
		}
		if msg.Contents["duration"] != int64(12) || msg.Contents["msg_type"] != int64(4) {
			t.Fatalf("contents = %#v", msg.Contents)
		}
	})
}

func TestMessageV4ParsesSystemNotificationWithoutPayload(t *testing.T) {
	msg := (&MessageV4{
		LocalType: int64(17)<<32 | MessageTypeSystemNotification,
		UserName:  "friend",
	}).Wrap("friend")
	if msg.Type != MessageTypeSystemNotification || msg.SubType != 17 {
		t.Fatalf("notification type = %d/%d", msg.Type, msg.SubType)
	}
	if msg.Content != "[系统通知]" || msg.PlainTextContent() != "[系统通知]" {
		t.Fatalf("notification content = %q / %q", msg.Content, msg.PlainTextContent())
	}
	if msg.Contents["notification_sub_type"] != int64(17) {
		t.Fatalf("notification contents = %#v", msg.Contents)
	}
}
