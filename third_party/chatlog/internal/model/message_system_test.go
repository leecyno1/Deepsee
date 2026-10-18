package model

import "testing"

func TestSystemMessageFallsBackToNonSysMsgSource(t *testing.T) {
	raw := `安全提示：<a href="https://example.com/help">查看详情</a>`
	msg := &Message{Type: MessageTypeSystem, Contents: map[string]interface{}{}}
	if err := msg.ParseMediaInfo(raw); err != nil {
		t.Fatal(err)
	}
	if msg.Content != raw {
		t.Fatalf("content = %q, want source text", msg.Content)
	}
}

func TestSystemMessageNormalizesRedEnvelopeReceiptMarkup(t *testing.T) {
	const sendID = "1000039801202607166186961679007"
	raw := `<img src="SystemMessages_HongbaoIcon.png"/>  你领取了left的<_wc_custom_link_ color="#FD9931" href="weixin://weixinhongbao/opendetail?sendid=` +
		sendID + `&sign=fixture&ver=6">红包</_wc_custom_link_>`
	msg := &Message{Type: MessageTypeSystem, Contents: map[string]interface{}{}}
	if err := msg.ParseMediaInfo(raw); err != nil {
		t.Fatal(err)
	}
	if msg.Content != "你领取了left的红包" {
		t.Fatalf("content = %q", msg.Content)
	}
	if msg.Contents["system_kind"] != "red_envelope_receipt" ||
		msg.Contents["hongbao_send_id"] != sendID ||
		msg.Contents["red_envelope_status"] != "已领取" {
		t.Fatalf("contents = %#v", msg.Contents)
	}
}
