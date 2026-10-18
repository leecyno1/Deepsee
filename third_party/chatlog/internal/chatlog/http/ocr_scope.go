package http

import (
	"strings"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ocr"
)

func ocrGlobalReceiveUpgradeRequested(config conf.OCRConfig, force bool) bool {
	return force || (config.AutoDownloadOriginal && config.ScopeAll())
}

func ocrScopedTalkers(config conf.OCRConfig) []string {
	if config.ScopeAll() {
		return nil
	}
	out := make([]string, 0, len(config.ListenContactsList())+len(config.ListenChatRoomsList()))
	out = append(out, config.ListenContactsList()...)
	out = append(out, config.ListenChatRoomsList()...)
	return out
}

// scopedOCRFileIDs returns every CDN identifier that WeChat may use for an
// image. This short-lived allowlist is a fallback for request layouts that do
// not expose the prearmed talker scope directly to the receive hook.
func scopedOCRFileIDs(refs []ocr.ImageRef) []string {
	out := make([]string, 0, len(refs)*3)
	seen := make(map[string]struct{}, len(refs)*3)
	for _, ref := range refs {
		for _, value := range []string{ref.CdnBigImgURL, ref.CdnMidImgURL, ref.CdnThumbURL} {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}
