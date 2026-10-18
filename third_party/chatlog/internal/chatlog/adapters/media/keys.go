package media

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/pkg/util/dat2img"
)

type xorRefreshCall struct {
	done chan struct{}
	key  byte
	err  error
}

type Keys struct {
	codecMu sync.RWMutex

	refreshMu sync.Mutex
	inflight  map[string]*xorRefreshCall
	scan      func(string) (byte, error)
}

func NewKeys() *Keys {
	return &Keys{
		inflight: make(map[string]*xorRefreshCall),
		scan:     dat2img.ScanAndSetXorKey,
	}
}

func (k *Keys) SetImageKey(key string) {
	k.codecMu.Lock()
	defer k.codecMu.Unlock()
	dat2img.SetAesKey(key)
}

func (k *Keys) RefreshXOR(dataDir string) (byte, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir != "" {
		dataDir = filepath.Clean(dataDir)
	}

	k.refreshMu.Lock()
	if call := k.inflight[dataDir]; call != nil {
		k.refreshMu.Unlock()
		<-call.done
		return call.key, call.err
	}
	call := &xorRefreshCall{done: make(chan struct{})}
	k.inflight[dataDir] = call
	scan := k.scan
	k.refreshMu.Unlock()

	k.codecMu.Lock()
	call.key, call.err = scan(dataDir)
	k.codecMu.Unlock()

	k.refreshMu.Lock()
	delete(k.inflight, dataDir)
	close(call.done)
	k.refreshMu.Unlock()
	return call.key, call.err
}

func (k *Keys) Decode(data []byte) ([]byte, string, error) {
	k.codecMu.RLock()
	defer k.codecMu.RUnlock()
	return dat2img.Dat2Image(data)
}

var _ ports.MediaKeys = (*Keys)(nil)
var _ ports.MediaCodec = (*Keys)(nil)
