package wcdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/wechatdb/wcdbapi"
)

type DataSource struct {
	dataDir string
	client  *wcdbapi.Client

	searchSchemaMu sync.RWMutex
	searchSchema   map[string][]searchTableMeta

	watchMu        sync.Mutex
	watcher        *fsnotify.Watcher
	watchedDirs    map[string]struct{}
	watchCallbacks map[string]map[uint64]func(event fsnotify.Event) error
	nextCallbackID uint64
	watchWG        sync.WaitGroup
}

type searchTableMeta struct {
	Name          string
	Columns       []string
	AllColumns    []searchColumnMeta
	DeepCandidate []searchColumnMeta
}

type searchColumnMeta struct {
	Name string
	Type string
}

func New(dataDir, dataKey string, newDecryptor wcdbapi.DecryptorFactory) (*DataSource, error) {
	c, err := wcdbapi.NewClient(dataDir, dataKey, newDecryptor)
	if err != nil {
		return nil, err
	}
	if err := c.OpenAccount(filepath.Join(c.DataDir(), "session", "session.db"), dataKey); err != nil {
		return nil, err
	}
	return &DataSource{
		dataDir:        c.DataDir(),
		client:         c,
		searchSchema:   make(map[string][]searchTableMeta),
		watchedDirs:    make(map[string]struct{}),
		watchCallbacks: make(map[string]map[uint64]func(event fsnotify.Event) error),
	}, nil
}

func toInt64(v interface{}) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

func toString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(v)
	}
}

func toBytes(v interface{}) []byte {
	switch t := v.(type) {
	case []byte:
		return t
	case string:
		return []byte(t)
	default:
		return nil
	}
}

func (ds *DataSource) SetCallback(group string, callback func(event fsnotify.Event) error) error {
	_, err := ds.SubscribeCallback(group, callback)
	return err
}

// SubscribeCallback registers a filesystem change callback and returns an
// idempotent cancellation function. SetCallback remains available for
// datasource-lifetime listeners, while shorter-lived HTTP/TUI consumers use
// this method so restarting a feature never accumulates stale callbacks.
func (ds *DataSource) SubscribeCallback(group string, callback func(event fsnotify.Event) error) (func(), error) {
	if callback == nil {
		return func() {}, nil
	}
	group = strings.ToLower(strings.TrimSpace(group))
	if group == "chatroom" {
		group = "contact"
	}
	switch group {
	case "message", "session", "contact":
	default:
		return nil, fmt.Errorf("unsupported callback group: %s", group)
	}
	dir := filepath.Join(ds.dataDir, group)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return nil, fmt.Errorf("watch %s: %w", group, err)
	}

	ds.watchMu.Lock()
	if ds.watcher == nil {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			ds.watchMu.Unlock()
			return nil, err
		}
		ds.watcher = watcher
		ds.watchWG.Add(1)
		go ds.runWatcher(watcher)
	}
	if _, ok := ds.watchedDirs[group]; !ok {
		if err := ds.watcher.Add(dir); err != nil {
			ds.watchMu.Unlock()
			return nil, err
		}
		ds.watchedDirs[group] = struct{}{}
	}
	ds.nextCallbackID++
	callbackID := ds.nextCallbackID
	if ds.watchCallbacks == nil {
		ds.watchCallbacks = make(map[string]map[uint64]func(event fsnotify.Event) error)
	}
	if ds.watchCallbacks[group] == nil {
		ds.watchCallbacks[group] = make(map[uint64]func(event fsnotify.Event) error)
	}
	ds.watchCallbacks[group][callbackID] = callback
	ds.watchMu.Unlock()

	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() {
			ds.watchMu.Lock()
			callbacks := ds.watchCallbacks[group]
			delete(callbacks, callbackID)
			if len(callbacks) == 0 {
				delete(ds.watchCallbacks, group)
			}
			ds.watchMu.Unlock()
		})
	}
	return cancel, nil
}

func (ds *DataSource) runWatcher(watcher *fsnotify.Watcher) {
	defer ds.watchWG.Done()
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			rel, err := filepath.Rel(ds.dataDir, event.Name)
			if err != nil {
				continue
			}
			group := strings.Split(filepath.ToSlash(rel), "/")[0]
			ds.watchMu.Lock()
			registered := ds.watchCallbacks[group]
			callbacks := make([]func(fsnotify.Event) error, 0, len(registered))
			for _, callback := range registered {
				callbacks = append(callbacks, callback)
			}
			ds.watchMu.Unlock()
			for _, callback := range callbacks {
				if err := callback(event); err != nil {
					log.Warn().Err(err).Str("group", group).Msg("wcdb file callback failed")
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Warn().Err(err).Msg("wcdb file watcher failed")
		}
	}
}
