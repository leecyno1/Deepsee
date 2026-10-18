package imgdownload

import (
	"fmt"
	"strings"
)

// nativeRequestKey matches the identity WeChat puts in the first string of a
// C2C image-download request: <talker>_<timestamp>_<local-id>_<variant>.
// Supplying the real message identity avoids colliding with an unrelated live
// request when an old thumbnail is retried.
func nativeRequestKey(req Request) string {
	talker := strings.TrimSpace(req.Talker)
	if talker != "" && req.MessageTime > 0 && req.DBLocalID > 0 {
		return fmt.Sprintf("%s_%d_%d_2", talker, req.MessageTime, req.DBLocalID)
	}
	if mediaKey := strings.TrimSpace(req.MediaKey); mediaKey != "" {
		return mediaKey
	}
	return strings.TrimSpace(req.CDN.PreferFileID())
}
