package http

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/model"
)

func (s *Service) enrichMessages(messages []*model.Message) {
	enrichRedEnvelopeStates(messages)
	for _, message := range messages {
		if message == nil {
			continue
		}
		message.RefreshProxyFields()
		s.enrichImageOCR(message)
		if message.Contents == nil {
			continue
		}
		recordInfo, ok := message.Contents["recordInfo"].(*model.RecordInfo)
		if !ok || recordInfo == nil {
			continue
		}
		assets := s.enrichRecordInfo(recordInfo, "")
		if len(assets) > 0 {
			message.Contents["assets"] = assets
		}
	}
}

func (s *Service) enrichImageOCR(message *model.Message) {
	if message == nil || message.Type != model.MessageTypeImage {
		return
	}
	runtime := s.currentOCRRuntime()
	if runtime == nil || runtime.store == nil {
		return
	}
	ref, ok := imageOCRRef(message)
	if !ok {
		return
	}
	record, err := runtime.store.Find(context.Background(), ref)
	if err != nil || record == nil {
		return
	}
	if message.Contents == nil {
		message.Contents = make(map[string]interface{})
	}
	message.Contents["ocr_status"] = record.Status
	message.Contents["ocr_index_id"] = record.ID
	message.Contents["ocr_attempts"] = record.Attempts
	if record.Error != "" {
		message.Contents["ocr_error"] = record.Error
	}
	if record.Status != ocr.StatusSucceeded {
		return
	}
	message.Contents["image_description"] = record.Description
	message.Contents["ocr_text"] = record.OCRText
	message.Contents["ocr_markdown"] = record.Markdown
	message.Contents["ocr_provider"] = record.Provider
	message.Contents["ocr_model"] = record.Model
	if len(record.Layout) > 0 {
		var layout any
		if json.Unmarshal(record.Layout, &layout) == nil {
			message.Contents["ocr_layout"] = layout
		}
	}
}

func enrichRedEnvelopeStates(messages []*model.Message) {
	claimed := make(map[string]string)
	for _, message := range messages {
		if message == nil || message.Type != model.MessageTypeSystem || message.Contents == nil {
			continue
		}
		if strings.TrimSpace(toString(message.Contents["system_kind"])) != "red_envelope_receipt" {
			continue
		}
		sendID := strings.TrimSpace(toString(message.Contents["hongbao_send_id"]))
		if sendID == "" {
			continue
		}
		status := strings.TrimSpace(toString(message.Contents["red_envelope_status"]))
		if status == "" {
			status = "已领取"
		}
		claimed[sendID] = status
	}
	if len(claimed) == 0 {
		return
	}
	for _, message := range messages {
		if message == nil || message.Type != model.MessageTypeShare ||
			(message.SubType != model.MessageSubTypeRedEnvelope &&
				message.SubType != model.MessageSubTypeRedEnvelopeCover) ||
			message.Contents == nil {
			continue
		}
		sendID := strings.TrimSpace(toString(message.Contents["pay_msg_id"]))
		if status := claimed[sendID]; sendID != "" && status != "" {
			message.Contents["red_envelope_status"] = status
		}
	}
}

func (s *Service) enrichRecordInfo(recordInfo *model.RecordInfo, prefix string) []model.RecordAsset {
	if recordInfo == nil {
		return nil
	}
	assets := make([]model.RecordAsset, 0, len(recordInfo.DataList.DataItems))
	for index := range recordInfo.DataList.DataItems {
		item := &recordInfo.DataList.DataItems[index]
		assetIndex := strconv.Itoa(index)
		if prefix != "" {
			assetIndex = prefix + "." + assetIndex
		}
		s.enrichRecordDataItem(item)
		if item.DataType == "17" && item.RecordXML != nil {
			nested := s.enrichRecordInfo(&item.RecordXML.RecordInfo, assetIndex)
			item.RecordXML.RecordInfo.Assets = nested
			assets = append(assets, nested...)
			continue
		}
		assets = append(assets, item.ToAsset(assetIndex))
	}
	recordInfo.Assets = assets
	return assets
}

func (s *Service) enrichRecordDataItem(item *model.DataItem) {
	if item == nil {
		return
	}
	switch item.DataType {
	case "2":
		setRecordItemMD5Proxy(item, "image")
	case "4":
		setRecordItemMD5Proxy(item, "video")
	case "8":
		if strings.TrimSpace(item.FullMD5) != "" {
			item.SetResolvedProxy("file", strings.TrimSpace(item.FullMD5), "fullmd5")
			return
		}
		name := strings.TrimSpace(item.DataTitle)
		if name == "" {
			item.SetUnresolvedProxy("file", "missing_fullmd5")
			return
		}
		media, err := s.db.GetMediaByName("file", name, item.DataSizeInt64())
		if err == nil && media != nil && strings.TrimSpace(media.Key) != "" {
			source := "hardlink_by_name"
			if item.DataSizeInt64() > 0 {
				source = "hardlink_by_name_size"
			}
			item.SetResolvedProxy("file", media.Key, source)
			return
		}
		item.SetUnresolvedProxy("file", "missing_fullmd5")
	case "3":
		item.SetUnresolvedProxy("voice", "record_voice_unresolved")
	}
}

func setRecordItemMD5Proxy(item *model.DataItem, mediaType string) {
	if key := strings.TrimSpace(item.FullMD5); key != "" {
		item.SetResolvedProxy(mediaType, key, "fullmd5")
		return
	}
	item.SetUnresolvedProxy(mediaType, "missing_fullmd5")
}
