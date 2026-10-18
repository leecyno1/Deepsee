package model

import (
	"net/url"
	"strconv"
	"strings"
)

type RecordAsset struct {
	Index          string `json:"index"`
	DataType       string `json:"dataType,omitempty"`
	DataFmt        string `json:"dataFmt,omitempty"`
	MessageType    int64  `json:"messageType"`
	MessageSubType int64  `json:"messageSubType"`
	Title          string `json:"title,omitempty"`
	Content        string `json:"content,omitempty"`
	SourceName     string `json:"sourceName,omitempty"`
	SourceTime     string `json:"sourceTime,omitempty"`
	Size           int64  `json:"size,omitempty"`
	ProxyType      string `json:"proxyType,omitempty"`
	ProxyKey       string `json:"proxyKey,omitempty"`
	ProxyURL       string `json:"proxyUrl,omitempty"`
	Resolved       bool   `json:"resolved"`
	KeySource      string `json:"keySource,omitempty"`
}

func BuildProxyURL(proxyType, key string) string {
	proxyType = strings.TrimSpace(proxyType)
	key = strings.TrimSpace(key)
	if proxyType == "" || key == "" {
		return ""
	}
	return "/" + proxyType + "/" + url.PathEscape(key)
}

func (m *Message) RefreshProxyFields() {
	if m == nil || m.Contents == nil {
		return
	}
	proxyType := ""
	proxyKey := ""
	keySource := ""
	switch m.Type {
	case MessageTypeImage:
		proxyType = "image"
		// Prefer the database-relative media path when it is available. The
		// path points at the concrete WeChat cache entry, while the MD5 index
		// can legitimately lag behind a newly received message.
		proxyKey, keySource = firstContentKey(m.Contents, "path", "md5")
	case MessageTypeVoice:
		proxyType = "voice"
		proxyKey, keySource = firstContentKey(m.Contents, "voice")
		if proxyKey == "" || proxyKey == "0" {
			proxyKey, keySource = firstContentKey(m.Contents, "voice_local_id")
		}
	case MessageTypeVideo:
		proxyType = "video"
		proxyKey, keySource = firstContentKey(m.Contents, "path", "md5")
	case MessageTypeShare:
		if m.SubType == MessageSubTypeFile {
			proxyType = "file"
			proxyKey, keySource = firstContentKey(m.Contents, "md5")
		}
	}
	if proxyType == "" {
		return
	}
	m.Contents["proxyType"] = proxyType
	m.Contents["proxyKey"] = proxyKey
	m.Contents["proxyUrl"] = BuildProxyURL(proxyType, proxyKey)
	m.Contents["resolved"] = proxyKey != ""
	m.Contents["keySource"] = keySource
}

func firstContentKey(contents map[string]interface{}, keys ...string) (string, string) {
	for _, key := range keys {
		value := strings.TrimSpace(toProxyString(contents[key]))
		if value != "" {
			return value, "contents." + key
		}
	}
	return "", ""
}

func toProxyString(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func (d *DataItem) DataSizeInt64() int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(d.DataSize), 10, 64)
	return value
}

func (d *DataItem) SourceTimeValue() string {
	if d.SourceTime != "" {
		return d.SourceTime
	}
	return d.SrcMsgCreateTime
}

func (d *DataItem) DefaultMessageType() (int64, int64) {
	switch d.DataType {
	case "1":
		return MessageTypeText, 0
	case "2":
		return MessageTypeImage, 0
	case "3":
		return MessageTypeVoice, 0
	case "4":
		return MessageTypeVideo, 0
	case "8":
		return MessageTypeShare, MessageSubTypeFile
	case "17":
		return MessageTypeShare, MessageSubTypeMergeForward
	default:
		return 0, 0
	}
}

func (d *DataItem) SetResolvedProxy(proxyType, key, keySource string) {
	d.ProxyType = proxyType
	d.ProxyKey = key
	d.ProxyURL = BuildProxyURL(proxyType, key)
	d.Resolved = key != ""
	d.KeySource = keySource
	if proxyType != "voice" {
		d.MD5 = key
	}
}

func (d *DataItem) SetUnresolvedProxy(proxyType, keySource string) {
	d.ProxyType = proxyType
	d.ProxyKey = ""
	d.ProxyURL = ""
	d.Resolved = false
	d.KeySource = keySource
	d.MD5 = ""
}

func (d *DataItem) ContentText() string {
	switch d.DataType {
	case "1":
		return d.DataDesc
	case "8":
		if d.DataTitle != "" {
			return "发送了文件：" + d.DataTitle
		}
		return "[文件]"
	case "2":
		return "[图片]"
	case "3":
		return "[语音]"
	case "4":
		return "[视频]"
	}
	if d.DataDesc != "" {
		return d.DataDesc
	}
	return d.DataTitle
}

func (d *DataItem) ToAsset(index string) RecordAsset {
	messageType, messageSubType := d.DefaultMessageType()
	return RecordAsset{
		Index:          index,
		DataType:       d.DataType,
		DataFmt:        d.DataFmt,
		MessageType:    messageType,
		MessageSubType: messageSubType,
		Title:          d.DataTitle,
		Content:        d.ContentText(),
		SourceName:     d.SourceName,
		SourceTime:     d.SourceTimeValue(),
		Size:           d.DataSizeInt64(),
		ProxyType:      d.ProxyType,
		ProxyKey:       d.ProxyKey,
		ProxyURL:       d.ProxyURL,
		Resolved:       d.Resolved,
		KeySource:      d.KeySource,
	}
}
