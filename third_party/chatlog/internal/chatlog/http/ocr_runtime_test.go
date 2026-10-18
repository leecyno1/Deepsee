package http

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ocr"
	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

type ocrMediaWatchConfig struct {
	lifecycleConfig
	dataDir string
}

func (f ocrMediaWatchConfig) GetDataDir() string { return f.dataDir }

type ocrScanDatabaseFixture struct {
	Database
	session  *model.Session
	sessions []*model.Session
	messages []*model.Message
}

func (f *ocrScanDatabaseFixture) Ready() bool { return true }

func (f *ocrScanDatabaseFixture) GetMedia(string, string) (*model.Media, error) {
	return nil, nil
}

func (f *ocrScanDatabaseFixture) GetSessions(string, int, int) (*ports.Sessions, error) {
	if len(f.sessions) > 0 {
		return &ports.Sessions{Items: f.sessions}, nil
	}
	return &ports.Sessions{Items: []*model.Session{f.session}}, nil
}

func (f *ocrScanDatabaseFixture) GetMessagesAfter(talker string, cursor model.MessageCursor, _ int) ([]*model.Message, error) {
	out := make([]*model.Message, 0)
	for _, message := range f.messages {
		if message.Talker == talker && cursor.BeforeMessage(message) {
			out = append(out, message)
		}
	}
	return out, nil
}

func TestOCRRealtimeScannerRespectsSelectedTalkers(t *testing.T) {
	store, err := ocr.OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	database := &ocrScanDatabaseFixture{
		sessions: []*model.Session{
			{UserName: "wxid_selected"},
			{UserName: "wxid_other"},
			{UserName: "100@chatroom"},
		},
		messages: []*model.Message{
			{Talker: "wxid_selected", Time: time.Unix(1001, 0), DBLocalID: 1, Type: model.MessageTypeImage, Contents: map[string]interface{}{"md5": "selected-image"}},
			{Talker: "wxid_other", Time: time.Unix(1001, 0), DBLocalID: 2, Type: model.MessageTypeImage, Contents: map[string]interface{}{"md5": "other-image"}},
			{Talker: "100@chatroom", Time: time.Unix(1001, 0), DBLocalID: 3, Type: model.MessageTypeImage, Contents: map[string]interface{}{"md5": "group-image"}},
		},
	}
	service := &Service{db: database}
	runtime := &imageOCRRuntime{
		config: conf.NormalizeOCRConfig(conf.OCRConfig{
			Provider:       conf.OCRProviderVLLM,
			ReceivedOnly:   true,
			ListenContacts: "wxid_selected",
		}),
		store:     store,
		ctx:       context.Background(),
		wake:      make(chan struct{}, 1),
		startedAt: 1000,
	}
	if err := store.SetCursor(context.Background(), "wxid_selected", 1000, int64(^uint64(0)>>1)); err != nil {
		t.Fatal(err)
	}
	service.scanNewImages(runtime)
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Pending != 0 || stats.WaitingMedia != 1 {
		t.Fatalf("scope queue stats = %#v", stats)
	}
}

func TestOCRGlobalReceiveUpgradeRespectsSelectedScope(t *testing.T) {
	selected := conf.NormalizeOCRConfig(conf.OCRConfig{
		AutoDownloadOriginal: true,
		ListenContacts:       "wxid_selected",
	})
	if ocrGlobalReceiveUpgradeRequested(selected, false) {
		t.Fatal("selected scope must not arm the process-wide receive upgrade")
	}
	all := conf.NormalizeOCRConfig(conf.OCRConfig{AutoDownloadOriginal: true})
	if !ocrGlobalReceiveUpgradeRequested(all, false) {
		t.Fatal("all-conversation scope should arm receive upgrade when enabled")
	}
	if !ocrGlobalReceiveUpgradeRequested(selected, true) {
		t.Fatal("explicit operations override should arm receive upgrade")
	}
}

func TestOCRMediaWatcherPromotesMidImageThatLandsLate(t *testing.T) {
	dataDir := t.TempDir()
	relativeBase := filepath.Join("msg", "attach", "session", "2026-08", "Img", "late-image")
	absoluteBase := filepath.Join(dataDir, relativeBase)
	if err := os.MkdirAll(filepath.Dir(absoluteBase), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absoluteBase+"_t.dat", []byte("thumbnail"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ocr.OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref := ocr.ImageRef{Talker: "wxid_wait", MessageTime: 100, DBLocalID: 1, MediaPath: relativeBase}
	if result, applyErr := store.ApplyScanBatchClassified(
		context.Background(), ref.Talker, nil, []ocr.ImageRef{ref}, ref.MessageTime, ref.DBLocalID,
	); applyErr != nil || result.WaitingMedia != 1 {
		t.Fatalf("waiting batch=%#v err=%v", result, applyErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	runtime := &imageOCRRuntime{
		ctx:            ctx,
		cancel:         cancel,
		store:          store,
		mediaWake:      make(chan struct{}, 1),
		mediaWatcher:   watcher,
		mediaWatchDirs: make(map[string]struct{}),
	}
	service := &Service{conf: ocrMediaWatchConfig{dataDir: dataDir}}
	runtime.wg.Add(1)
	go service.runOCRMediaWatcher(runtime)
	defer func() {
		cancel()
		runtime.wg.Wait()
		_ = watcher.Close()
	}()

	waiting, promoted, err := service.refreshOCRMediaGate(runtime)
	if err != nil || waiting != 1 || promoted != 0 {
		t.Fatalf("initial gate waiting=%d promoted=%d err=%v", waiting, promoted, err)
	}
	if err := os.WriteFile(absoluteBase+".dat", []byte("mid-image"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.mediaWake:
	case <-time.After(2 * time.Second):
		t.Fatal("late mid image did not wake media gate")
	}
	waiting, promoted, err = service.refreshOCRMediaGate(runtime)
	if err != nil || waiting != 0 || promoted != 1 {
		t.Fatalf("late gate waiting=%d promoted=%d err=%v", waiting, promoted, err)
	}
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Pending != 1 || stats.WaitingMedia != 0 {
		t.Fatalf("promoted stats=%#v err=%v", stats, err)
	}
}

func TestScopedOCRFileIDsIncludesAllCDNVariants(t *testing.T) {
	got := scopedOCRFileIDs([]ocr.ImageRef{
		{CdnBigImgURL: "big", CdnMidImgURL: "mid", CdnThumbURL: "thumb"},
		{CdnMidImgURL: "mid", CdnThumbURL: " thumb "},
	})
	want := []string{"big", "mid", "thumb"}
	if len(got) != len(want) {
		t.Fatalf("file ids=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("file ids=%v want=%v", got, want)
		}
	}
}

func TestOCRScopedTalkersAreAvailableBeforeMessageScan(t *testing.T) {
	got := ocrScopedTalkers(conf.OCRConfig{
		ListenContacts:  "wxid_contact",
		ListenChatRooms: "12345@chatroom,67890@chatroom",
	})
	want := []string{"wxid_contact", "12345@chatroom", "67890@chatroom"}
	if len(got) != len(want) {
		t.Fatalf("talkers=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("talkers=%v want=%v", got, want)
		}
	}
	if got := ocrScopedTalkers(conf.OCRConfig{}); len(got) != 0 {
		t.Fatalf("all-scope talkers=%v, want empty", got)
	}
}

func TestOCRFirstEnableUsesActivationWatermark(t *testing.T) {
	store, err := ocr.OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	database := &ocrScanDatabaseFixture{
		session: &model.Session{UserName: "wxid_fixture", NTime: time.Unix(500, 0)},
		messages: []*model.Message{{
			Talker: "wxid_fixture", Time: time.Unix(900, 0), DBLocalID: 7,
			Type: model.MessageTypeImage, Contents: map[string]interface{}{"md5": "old-image"},
		}},
	}
	service := &Service{db: database}
	runtime := &imageOCRRuntime{
		config:    conf.OCRConfig{Provider: conf.OCRProviderVLLM, ReceivedOnly: true},
		store:     store,
		ctx:       context.Background(),
		wake:      make(chan struct{}, 1),
		startedAt: 1000,
	}

	service.scanNewImages(runtime)
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 0 {
		t.Fatalf("historical image was enqueued on first enable: %#v", stats)
	}
	timestamp, localID, found, err := store.Cursor(context.Background(), "wxid_fixture")
	if err != nil || !found || timestamp != 1000 || localID != int64(^uint64(0)>>1) {
		t.Fatalf("activation cursor = %d/%d found=%v err=%v", timestamp, localID, found, err)
	}

	database.messages = append(database.messages, &model.Message{
		Talker: "wxid_fixture", Time: time.Unix(1001, 0), DBLocalID: 1,
		Type: model.MessageTypeImage, Contents: map[string]interface{}{"md5": "new-image"},
	})
	service.scanNewImages(runtime)
	stats, err = store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Pending != 0 || stats.WaitingMedia != 1 {
		t.Fatalf("new image queue stats = %#v", stats)
	}
}

func TestOCRFirstEventScanIncludesMessagesAfterActivation(t *testing.T) {
	store, err := ocr.OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	database := &ocrScanDatabaseFixture{
		session: &model.Session{UserName: "wxid_fixture"},
		messages: []*model.Message{{
			Talker: "wxid_fixture", Time: time.Unix(1001, 0), DBLocalID: 1,
			Type: model.MessageTypeImage, Contents: map[string]interface{}{"md5": "first-event-image"},
		}},
	}
	service := &Service{db: database}
	runtime := &imageOCRRuntime{
		config:    conf.OCRConfig{Provider: conf.OCRProviderVLLM, ReceivedOnly: true},
		store:     store,
		ctx:       context.Background(),
		wake:      make(chan struct{}, 1),
		startedAt: 1000,
	}
	service.scanNewImages(runtime)
	stats, err := store.Stats(context.Background())
	if err != nil || stats.Pending != 0 || stats.WaitingMedia != 1 {
		t.Fatalf("first event queue stats=%#v err=%v", stats, err)
	}
}

type ocrPathConfig struct {
	lifecycleConfig
	root    string
	account string
}

func (config ocrPathConfig) GetRuntimeDir() string { return config.root }
func (config ocrPathConfig) GetAccount() string    { return config.account }
func (ocrPathConfig) GetOCRConfig() conf.OCRConfig { return conf.DefaultOCRConfig() }

func TestOCRIndexPathIsAccountScoped(t *testing.T) {
	root := t.TempDir()
	service := &Service{conf: ocrPathConfig{root: root, account: "wxid_fixture_suffix"}}
	want := filepath.Join(root, "workspaces", "wxid_fixture_suffix", "ocr", "image_ocr.db")
	if got := service.ocrIndexPath(); got != want {
		t.Fatalf("index path = %q, want %q", got, want)
	}
}

func TestOCRSearchPrefersPathBackedImagePreview(t *testing.T) {
	record := ocr.Record{
		ImageRef: ocr.ImageRef{
			MediaKey:  "44080f68b776e6a50a9ad33f59688387",
			MediaPath: "msg/attach/account/2026-07/Img/image",
		},
	}
	row := toHistoryMessage(ocrRecordMessage(record), "127.0.0.1:5030")
	preferOCRMediaPath(row, record, "127.0.0.1:5030")

	if got := row["image_key"]; got != record.MediaPath {
		t.Fatalf("image_key = %q", got)
	}
	if got := row["image_url"]; got != "http://127.0.0.1:5030/image/msg/attach/account/2026-07/Img/image" {
		t.Fatalf("image_url = %q", got)
	}
	keys, ok := row["image_keys"].([]string)
	if !ok || len(keys) != 2 || keys[1] != record.MediaKey {
		t.Fatalf("image_keys = %#v", row["image_keys"])
	}
}

func TestOCRReloadsImageThatIsStillBeingWritten(t *testing.T) {
	dataDir := t.TempDir()
	relativeBase := filepath.Join("msg", "attach", "session", "2026-08", "Img", "settling")
	absolutePath := filepath.Join(dataDir, relativeBase+".jpg")
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	complete := encoded.Bytes()
	if err := os.WriteFile(absolutePath, complete[:len(complete)-206], 0o600); err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(stdhttp.HandlerFunc(func(response stdhttp.ResponseWriter, request *stdhttp.Request) {
		requests++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"markdown_result":"自动识别成功"}`))
	}))
	defer server.Close()
	client, err := ocr.NewClient(conf.OCRConfig{
		Enabled: true, Provider: conf.OCRProviderVLLM, Endpoint: server.URL,
		Model: "glm-ocr", RequestTimeout: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runtime := &imageOCRRuntime{ctx: ctx, client: client}
	record := &ocr.Record{ID: 72, ImageRef: ocr.ImageRef{Talker: "wxid_fixture", MediaPath: relativeBase + ".jpg"}}
	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		writeDone <- os.WriteFile(absolutePath, complete, 0o600)
	}()
	service := &Service{conf: ocrMediaWatchConfig{dataDir: dataDir}}
	result, err := service.recognizeOCRAfterImageSettles(runtime, record)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := <-writeDone; writeErr != nil {
		t.Fatal(writeErr)
	}
	if requests != 1 || result.Text != "自动识别成功" {
		t.Fatalf("requests=%d result=%#v", requests, result)
	}
}
