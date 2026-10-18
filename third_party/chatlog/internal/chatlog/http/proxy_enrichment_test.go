package http

import (
	"testing"

	"github.com/sjzar/chatlog/internal/model"
)

func TestEnrichRecordInfoFlattensNestedAssets(t *testing.T) {
	record := &model.RecordInfo{DataList: model.DataList{DataItems: []model.DataItem{
		{DataType: "2", FullMD5: "image-md5"},
		{DataType: "17", RecordXML: &model.RecordXML{RecordInfo: model.RecordInfo{DataList: model.DataList{DataItems: []model.DataItem{
			{DataType: "4", FullMD5: "video-md5"},
		}}}}},
	}}}

	service := &Service{}
	assets := service.enrichRecordInfo(record, "")
	if len(assets) != 2 {
		t.Fatalf("asset count = %d, want 2", len(assets))
	}
	if assets[0].Index != "0" || assets[0].ProxyURL != "/image/image-md5" {
		t.Fatalf("unexpected first asset: %#v", assets[0])
	}
	if assets[1].Index != "1.0" || assets[1].ProxyURL != "/video/video-md5" {
		t.Fatalf("unexpected nested asset: %#v", assets[1])
	}
}

func TestEnrichMessagesAssociatesRedEnvelopeReceipt(t *testing.T) {
	const sendID = "1000039801202607166186961679007"
	envelope := &model.Message{
		Type:    model.MessageTypeShare,
		SubType: model.MessageSubTypeRedEnvelope,
		Contents: map[string]interface{}{
			"pay_msg_id":          sendID,
			"red_envelope_status": "领取红包",
		},
	}
	receipt := &model.Message{
		Type: model.MessageTypeSystem,
		Contents: map[string]interface{}{
			"system_kind":         "red_envelope_receipt",
			"hongbao_send_id":     sendID,
			"red_envelope_status": "已领取",
		},
	}

	(&Service{}).enrichMessages([]*model.Message{envelope, receipt})
	if envelope.Contents["red_envelope_status"] != "已领取" {
		t.Fatalf("envelope contents = %#v", envelope.Contents)
	}
}
