// Package filecopy exposes the cache location retained for compatibility with
// existing runtime state. The former global copy-manager implementation had no
// production consumers and was removed to keep cache ownership explicit.
package filecopy

import "github.com/sjzar/chatlog/pkg/util"

func GetCacheDir() string {
	return util.DefaultCacheDir("filecopy")
}
