package hermespush

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sjzar/chatlog/pkg/util"
)

func materializeHermesBridgeAsset(name, script string) (string, func(), error) {
	content := []byte(script)
	if len(content) == 0 {
		return "", nil, fmt.Errorf("embedded Hermes bridge %s is empty", name)
	}
	digest := sha256.Sum256(content)
	dir := util.DefaultCacheDir("runtime_assets")
	path := filepath.Join(dir, fmt.Sprintf("%s-%x.py", name, digest[:8]))
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		_ = os.Chmod(path, 0o600)
		return path, func() {}, nil
	}
	if err := util.WritePrivateFileAtomic(path, content); err != nil {
		return "", nil, err
	}
	return path, func() {}, nil
}
