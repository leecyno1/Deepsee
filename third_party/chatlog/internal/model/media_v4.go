package model

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

type MediaV4 struct {
	Type         string `json:"type"`
	Key          string `json:"key"`
	Dir1         string `json:"dir1"`
	Dir2         string `json:"dir2"`
	ExtraBuffer  string `json:"extraBuffer"` // Extra buffer for Rec subdirectory
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	ModifyTime   int64  `json:"modifyTime"`
	HardLinkType int64  `json:"-"`
}

func (m *MediaV4) Wrap() *Media {

	var path string
	switch m.Type {
	case "image":
		extraParts := m.extraBufferParts()
		if m.HardLinkType == 4 && len(extraParts) > 0 {
			path = filepath.Join("msg", "attach", m.Dir1, m.Dir2, "Rec", extraParts[0], "Img", m.Name)
		} else {
			path = filepath.Join("msg", "attach", m.Dir1, m.Dir2, "Img", m.Name)
		}
	case "video":
		extraParts := m.extraBufferParts()
		if m.HardLinkType == 5 && len(extraParts) > 0 {
			path = filepath.Join("msg", "attach", m.Dir1, m.Dir2, "Rec", extraParts[0], "V", m.Name)
		} else {
			path = filepath.Join("msg", "video", m.Dir1, m.Name)
		}
	case "file":
		extraParts := m.extraBufferParts()
		if m.HardLinkType == 6 && len(extraParts) > 0 {
			directory := "F"
			if filepath.Ext(m.Name) == "" {
				directory = "Dat"
			}
			if len(extraParts) > 1 {
				path = filepath.Join("msg", "attach", m.Dir1, m.Dir2, "Rec", extraParts[0], directory, extraParts[1], m.Name)
			} else {
				path = filepath.Join("msg", "attach", m.Dir1, m.Dir2, "Rec", extraParts[0], directory, m.Name)
			}
		} else {
			path = filepath.Join("msg", "file", m.Dir1, m.Name)
		}
	}

	return &Media{
		Type:       m.Type,
		Key:        m.Key,
		Path:       path,
		Name:       m.Name,
		Size:       m.Size,
		ModifyTime: m.ModifyTime,
	}
}

func (m *MediaV4) extraBufferParts() []string {
	if m.ExtraBuffer == "" {
		return nil
	}
	re := regexp.MustCompile(`[a-zA-Z0-9]+`)
	parts := make([]string, 0, 4)
	appendParts := func(value string) {
		for _, part := range re.FindAllString(value, -1) {
			if strings.TrimSpace(part) != "" {
				parts = append(parts, part)
			}
		}
	}
	raw := []byte(m.ExtraBuffer)
	if isPrintableMediaExtra(raw) {
		appendParts(m.ExtraBuffer)
		return parts
	}
	for len(raw) > 0 {
		_, wireType, tagLength := protowire.ConsumeTag(raw)
		if tagLength < 0 {
			break
		}
		raw = raw[tagLength:]
		var fieldLength int
		switch wireType {
		case protowire.VarintType:
			_, fieldLength = protowire.ConsumeVarint(raw)
		case protowire.Fixed32Type:
			_, fieldLength = protowire.ConsumeFixed32(raw)
		case protowire.Fixed64Type:
			_, fieldLength = protowire.ConsumeFixed64(raw)
		case protowire.BytesType:
			var value []byte
			value, fieldLength = protowire.ConsumeBytes(raw)
			if fieldLength >= 0 && isPrintableMediaExtra(value) {
				appendParts(string(value))
			}
		default:
			return parts
		}
		if fieldLength < 0 || fieldLength > len(raw) {
			break
		}
		raw = raw[fieldLength:]
	}
	return parts
}

func isPrintableMediaExtra(value []byte) bool {
	if len(value) == 0 || !utf8.Valid(value) {
		return false
	}
	for _, character := range string(value) {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
