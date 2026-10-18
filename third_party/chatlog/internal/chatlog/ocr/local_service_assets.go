package ocr

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sjzar/chatlog/pkg/util"
)

// The managed OCR launcher used to be discovered only from deploy/ocr. A
// release binary started outside the repository therefore had no installer,
// configuration, start or stop script. Keep the complete launcher set in the
// binary and materialize one content-addressed copy on first use.
//
//go:embed assets/start-macos-mlx.sh assets/stop-macos-mlx.sh assets/install-macos-mlx.sh assets/config.macos-mlx.yaml
var embeddedLocalServiceAssets embed.FS

type localServiceAsset struct {
	name string
	mode os.FileMode
}

var localServiceAssetManifest = []localServiceAsset{
	{name: "start-macos-mlx.sh", mode: 0o700},
	{name: "stop-macos-mlx.sh", mode: 0o700},
	{name: "install-macos-mlx.sh", mode: 0o700},
	{name: "config.macos-mlx.yaml", mode: 0o600},
}

func materializeEmbeddedLocalServiceAssets() (string, string, error) {
	digest := sha256.New()
	contents := make(map[string][]byte, len(localServiceAssetManifest))
	for _, asset := range localServiceAssetManifest {
		content, err := embeddedLocalServiceAssets.ReadFile(filepath.ToSlash(filepath.Join("assets", asset.name)))
		if err != nil {
			return "", "", fmt.Errorf("read embedded OCR asset %s: %w", asset.name, err)
		}
		if len(content) == 0 {
			return "", "", fmt.Errorf("embedded OCR asset %s is empty", asset.name)
		}
		contents[asset.name] = content
		_, _ = digest.Write([]byte(asset.name))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(content)
		_, _ = digest.Write([]byte{0})
	}

	dir := filepath.Join(
		util.DefaultCacheDir("runtime_assets"),
		fmt.Sprintf("ocr-macos-mlx-%x", digest.Sum(nil)[:8]),
	)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	for _, asset := range localServiceAssetManifest {
		if err := materializeLocalServiceAsset(dir, asset, contents[asset.name]); err != nil {
			return "", "", err
		}
	}
	return filepath.Join(dir, "start-macos-mlx.sh"), filepath.Join(dir, "stop-macos-mlx.sh"), nil
}

func materializeLocalServiceAsset(dir string, asset localServiceAsset, content []byte) error {
	path := filepath.Join(dir, asset.name)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return os.Chmod(path, asset.mode)
	}
	temporary, err := os.CreateTemp(dir, "."+asset.name+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(asset.mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
