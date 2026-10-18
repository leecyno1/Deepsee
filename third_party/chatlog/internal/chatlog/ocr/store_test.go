package ocr

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStoreQueueIndexSearchAndCursor(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "ocr", "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ref := ImageRef{
		Talker: "wxid_fixture", TalkerName: "测试会话",
		Sender: "wxid_sender", SenderName: "发送者",
		MessageTime: 1_785_000_001, MessageSeq: 123, DBLocalID: 7, MessageID: 123,
		MediaKey: "0123456789abcdef", MediaPath: "msg/attach/image.dat",
	}
	id, inserted, err := store.Enqueue(ctx, ref)
	if err != nil || !inserted || id <= 0 {
		t.Fatalf("enqueue id=%d inserted=%v err=%v", id, inserted, err)
	}
	if _, insertedAgain, err := store.Enqueue(ctx, ref); err != nil || insertedAgain {
		t.Fatalf("second enqueue inserted=%v err=%v", insertedAgain, err)
	}
	next, err := store.NextPending(ctx)
	if err != nil || next == nil || next.ID != id {
		t.Fatalf("next = %#v err=%v", next, err)
	}
	if err := store.MarkProcessing(ctx, id); err != nil {
		t.Fatal(err)
	}
	layout := json.RawMessage(`[[{"label":"title","content":"季度经营报告"}]]`)
	if err := store.MarkSucceeded(
		ctx, id, strings.Repeat("a", 64), "vllm", "glm-ocr",
		"图片包含标题；识别文字：季度经营报告 营业收入增长",
		"季度经营报告\n营业收入增长 23%", "# 季度经营报告\n营业收入增长 23%", layout,
	); err != nil {
		t.Fatal(err)
	}

	found, err := store.Find(ctx, ref)
	if err != nil || found == nil || found.Status != StatusSucceeded {
		t.Fatalf("found = %#v err=%v", found, err)
	}
	result, err := store.Search(ctx, SearchRequest{Keyword: "经营报告", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Records) != 1 || result.Records[0].ID != id {
		t.Fatalf("search = %#v", result)
	}
	short, err := store.Search(ctx, SearchRequest{Keyword: "收入", Talkers: []string{"wxid_fixture"}, Limit: 20})
	if err != nil || short.Total != 1 {
		t.Fatalf("short search = %#v err=%v", short, err)
	}
	miss, err := store.Search(ctx, SearchRequest{Keyword: "收入", Talkers: []string{"wxid_other"}, Limit: 20})
	if err != nil || miss.Total != 0 {
		t.Fatalf("filtered search = %#v err=%v", miss, err)
	}

	if err := store.SetCursor(ctx, ref.Talker, ref.MessageTime, ref.DBLocalID); err != nil {
		t.Fatal(err)
	}
	messageTime, localID, ok, err := store.Cursor(ctx, ref.Talker)
	if err != nil || !ok || messageTime != ref.MessageTime || localID != ref.DBLocalID {
		t.Fatalf("cursor = %d/%d found=%v err=%v", messageTime, localID, ok, err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Succeeded != 1 || stats.Searchable != 1 || stats.IndexPath == "" {
		t.Fatalf("stats = %#v", stats)
	}
	recent, err := store.Recent(ctx, 20)
	if err != nil || len(recent) != 1 || recent[0].ID != id ||
		recent[0].Description == "" || recent[0].Status != StatusSucceeded {
		t.Fatalf("recent = %#v err=%v", recent, err)
	}
}

func TestStoreStatsEmpty(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 0 || stats.Searchable != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestStoreApplyScanBatchQueuesBurstAndAdvancesCursor(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const count = 500
	refs := make([]ImageRef, 0, count)
	for i := 1; i <= count; i++ {
		refs = append(refs, ImageRef{
			Talker:      "wxid_burst",
			MessageTime: 1_785_100_000 + int64(i),
			DBLocalID:   int64(i),
			MediaKey:    "burst-" + strconv.Itoa(i),
			MediaPath:   "msg/attach/burst/" + strconv.Itoa(i) + ".dat",
		})
	}
	inserted, err := store.ApplyScanBatch(
		ctx, "wxid_burst", refs, refs[len(refs)-1].MessageTime, refs[len(refs)-1].DBLocalID,
	)
	if err != nil || inserted != count {
		t.Fatalf("burst insert count=%d err=%v", inserted, err)
	}
	stats, err := store.Stats(ctx)
	if err != nil || stats.Pending != count {
		t.Fatalf("burst stats=%#v err=%v", stats, err)
	}
	messageTime, localID, found, err := store.Cursor(ctx, "wxid_burst")
	if err != nil || !found || messageTime != refs[len(refs)-1].MessageTime || localID != count {
		t.Fatalf("burst cursor=%d/%d found=%v err=%v", messageTime, localID, found, err)
	}
	inserted, err = store.ApplyScanBatch(
		ctx, "wxid_burst", refs, refs[len(refs)-1].MessageTime, refs[len(refs)-1].DBLocalID,
	)
	if err != nil || inserted != 0 {
		t.Fatalf("duplicate burst insert count=%d err=%v", inserted, err)
	}
}

func TestStoreClassifiedBatchKeepsThumbnailOutOfOCRQueue(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending := ImageRef{Talker: "wxid_gate", MessageTime: 100, DBLocalID: 1, MediaKey: "mid"}
	waiting := ImageRef{Talker: "wxid_gate", MessageTime: 101, DBLocalID: 2, MediaKey: "thumb"}
	result, err := store.ApplyScanBatchClassified(ctx, "wxid_gate", []ImageRef{pending}, []ImageRef{waiting}, 101, 2)
	if err != nil || result.Pending != 1 || result.WaitingMedia != 1 {
		t.Fatalf("classified result=%#v err=%v", result, err)
	}
	stats, err := store.Stats(ctx)
	if err != nil || stats.Pending != 1 || stats.WaitingMedia != 1 {
		t.Fatalf("classified stats=%#v err=%v", stats, err)
	}
	next, err := store.NextPending(ctx)
	if err != nil || next == nil || next.MediaKey != "mid" {
		t.Fatalf("next pending=%#v err=%v", next, err)
	}
	records, err := store.MediaGateRecords(ctx, 10)
	if err != nil || len(records) != 2 {
		t.Fatalf("media gate records=%#v err=%v", records, err)
	}
	var waitingID int64
	for _, record := range records {
		if record.Status == StatusWaitingMedia {
			waitingID = record.ID
		}
	}
	if waitingID == 0 {
		t.Fatal("waiting-media record missing")
	}
	if changed, err := store.MarkMediaReady(ctx, waitingID); err != nil || !changed {
		t.Fatalf("promote waiting record changed=%v err=%v", changed, err)
	}
	stats, err = store.Stats(ctx)
	if err != nil || stats.Pending != 2 || stats.WaitingMedia != 0 {
		t.Fatalf("promoted stats=%#v err=%v", stats, err)
	}
}

func TestStorePersistsWaitingMediaRetrySchedule(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ref := ImageRef{
		Talker: "wxid_media_retry", MessageTime: 102, DBLocalID: 3,
		MediaKey: "retry-media", CdnMidImgURL: "cdn-fixture",
	}
	result, err := store.ApplyScanBatchClassified(ctx, ref.Talker, nil, []ImageRef{ref}, ref.MessageTime, ref.DBLocalID)
	if err != nil || result.WaitingMedia != 1 {
		t.Fatalf("classified result=%#v err=%v", result, err)
	}
	waiting, err := store.WaitingMediaRecords(ctx, 10)
	if err != nil || len(waiting) != 1 {
		t.Fatalf("waiting records=%#v err=%v", waiting, err)
	}
	id := waiting[0].ID
	due, err := store.DueMediaRetryRecords(ctx, time.Now().Unix(), 8, 10)
	if err != nil || len(due) != 1 || due[0].ID != id {
		t.Fatalf("due records=%#v err=%v", due, err)
	}
	nextAt := time.Now().Add(time.Hour).Unix()
	if changed, markErr := store.MarkMediaRetry(ctx, id, nextAt, "自动重试已布防"); markErr != nil || !changed {
		t.Fatalf("mark retry changed=%v err=%v", changed, markErr)
	}
	record, err := store.Get(ctx, id)
	if err != nil || record == nil || record.MediaRetryAttempts != 1 ||
		record.MediaNextRetryAt != nextAt || record.MediaLastRetryAt == 0 || record.MediaRetryError == "" {
		t.Fatalf("retry record=%#v err=%v", record, err)
	}
	due, err = store.DueMediaRetryRecords(ctx, time.Now().Unix(), 8, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("future retry unexpectedly due=%#v err=%v", due, err)
	}
	next, exists, err := store.NextMediaRetryAt(ctx, 8)
	if err != nil || !exists || next.Unix() != nextAt {
		t.Fatalf("next media retry=%v exists=%v err=%v", next, exists, err)
	}
	if changed, markErr := store.MarkMediaReady(ctx, id); markErr != nil || !changed {
		t.Fatalf("promote retry changed=%v err=%v", changed, markErr)
	}
	record, err = store.Get(ctx, id)
	if err != nil || record == nil || record.Status != StatusPending ||
		record.MediaNextRetryAt != 0 || record.MediaRetryError != "" {
		t.Fatalf("promoted retry record=%#v err=%v", record, err)
	}
}

func TestStoreNextWorkAtUsesQueueDeadline(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, exists, err := store.NextWorkAt(ctx); err != nil || exists {
		t.Fatalf("empty queue deadline exists=%v err=%v", exists, err)
	}
	ref := ImageRef{Talker: "wxid_due", MessageTime: time.Now().Unix(), DBLocalID: 1, MediaKey: "due"}
	id, _, err := store.Enqueue(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	next, exists, err := store.NextWorkAt(ctx)
	if err != nil || !exists || next.After(time.Now()) {
		t.Fatalf("pending deadline=%v exists=%v err=%v", next, exists, err)
	}
	if err := store.MarkProcessing(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, id, errors.New("retry later")); err != nil {
		t.Fatal(err)
	}
	next, exists, err = store.NextWorkAt(ctx)
	if err != nil || !exists || time.Until(next) < 10*time.Second {
		t.Fatalf("retry deadline=%v exists=%v err=%v", next, exists, err)
	}
}

func TestStoreRetriesConnectionFailuresWhenLocalServiceBecomesReady(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref := ImageRef{
		Talker: "wxid_fixture", MessageTime: 1_785_000_002, DBLocalID: 8,
		MediaKey: "fedcba9876543210",
	}
	id, _, err := store.Enqueue(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessing(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, id, errors.New("dial tcp 127.0.0.1:8081: connect: connection refused")); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryConnectionFailures(ctx)
	if err != nil || retried != 1 {
		t.Fatalf("retried=%d err=%v", retried, err)
	}
	record, err := store.NextPending(ctx)
	if err != nil || record == nil || record.ID != id || record.Status != StatusPending {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}

func TestStoreSyncsUnfinishedTasksToNewOCRTarget(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	newRef := func(localID int64, mediaKey string) ImageRef {
		return ImageRef{
			Talker: "wxid_fixture", MessageTime: 1_785_000_100 + localID,
			DBLocalID: localID, MediaKey: mediaKey,
		}
	}
	pendingRef := newRef(1, "pending")
	pendingID, _, err := store.Enqueue(ctx, pendingRef)
	if err != nil {
		t.Fatal(err)
	}
	failedRef := newRef(2, "failed")
	failedID, _, err := store.Enqueue(ctx, failedRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessing(ctx, failedID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, failedID, errors.New("old provider failed")); err != nil {
		t.Fatal(err)
	}
	processingRef := newRef(3, "processing")
	processingID, _, err := store.Enqueue(ctx, processingRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessing(ctx, processingID); err != nil {
		t.Fatal(err)
	}
	succeededRef := newRef(4, "succeeded")
	succeededID, _, err := store.Enqueue(ctx, succeededRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessing(ctx, succeededID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSucceeded(
		ctx, succeededID, "hash", "vllm", "old-model",
		"done", "done", "done", nil,
	); err != nil {
		t.Fatal(err)
	}

	synced, err := store.SyncRetryableTarget(ctx, "maas", "glm-ocr")
	if err != nil || synced != 3 {
		t.Fatalf("synced=%d err=%v", synced, err)
	}
	for _, fixture := range []struct {
		ref ImageRef
		id  int64
	}{
		{pendingRef, pendingID},
		{failedRef, failedID},
		{processingRef, processingID},
	} {
		record, findErr := store.Find(ctx, fixture.ref)
		if findErr != nil || record == nil {
			t.Fatalf("record %d = %#v err=%v", fixture.id, record, findErr)
		}
		if record.Status != StatusPending || record.Provider != "maas" ||
			record.Model != "glm-ocr" || record.Error != "" || record.NextAttemptAt != 0 {
			t.Fatalf("synced record %d = %#v", fixture.id, record)
		}
	}
	succeeded, err := store.Find(ctx, succeededRef)
	if err != nil || succeeded == nil || succeeded.Status != StatusSucceeded ||
		succeeded.Provider != "vllm" || succeeded.Model != "old-model" {
		t.Fatalf("succeeded record = %#v err=%v", succeeded, err)
	}
}

func TestStoreReprocessesSucceededRecordAndRemovesStaleSearchText(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ref := ImageRef{
		Talker: "wxid_fixture", MessageTime: 1_785_000_900,
		DBLocalID: 9, MediaKey: "rerecognize-fixture",
	}
	id, _, err := store.Enqueue(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessing(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSucceeded(
		ctx, id, "old-hash", "vllm", "old-model",
		"旧识别描述", "历史发票号码 123", "# 历史发票号码 123", json.RawMessage(`[{"old":true}]`),
	); err != nil {
		t.Fatal(err)
	}
	before, err := store.Search(ctx, SearchRequest{Keyword: "发票号码", Limit: 20})
	if err != nil || before.Total != 1 {
		t.Fatalf("search before reprocess = %#v err=%v", before, err)
	}

	requeued, err := store.Reprocess(ctx, id, "maas", "glm-ocr")
	if err != nil || !requeued {
		t.Fatalf("requeued=%v err=%v", requeued, err)
	}
	record, err := store.Find(ctx, ref)
	if err != nil || record == nil {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	if record.Status != StatusPending || record.Provider != "maas" || record.Model != "glm-ocr" ||
		record.ContentHash != "" || record.Description != "" || record.OCRText != "" ||
		record.Markdown != "" || len(record.Layout) != 0 || record.Attempts != 0 {
		t.Fatalf("reprocessed record = %#v", record)
	}
	after, err := store.Search(ctx, SearchRequest{Keyword: "发票号码", Limit: 20})
	if err != nil || after.Total != 0 {
		t.Fatalf("search after reprocess = %#v err=%v", after, err)
	}
	if requeuedAgain, err := store.Reprocess(ctx, id, "maas", "glm-ocr"); err != nil || requeuedAgain {
		t.Fatalf("second reprocess requeued=%v err=%v", requeuedAgain, err)
	}
}

func TestStoreGetsFullRecordAndClearsOnlySucceededContent(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "image_ocr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	succeededRef := ImageRef{Talker: "wxid_fixture", MessageTime: 1_785_002_000, DBLocalID: 20, MediaKey: "clear-success"}
	succeededID, _, err := store.Enqueue(ctx, succeededRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSucceeded(ctx, succeededID, "hash", "vllm", "glm-ocr", "完整描述", "完整文字", "# 完整文字", json.RawMessage(`[{"label":"text"}]`)); err != nil {
		t.Fatal(err)
	}
	pendingRef := ImageRef{Talker: "wxid_fixture", MessageTime: 1_785_002_001, DBLocalID: 21, MediaKey: "keep-pending"}
	pendingID, _, err := store.Enqueue(ctx, pendingRef)
	if err != nil {
		t.Fatal(err)
	}
	full, err := store.Get(ctx, succeededID)
	if err != nil || full == nil || full.OCRText != "完整文字" || full.Markdown != "# 完整文字" || len(full.Layout) == 0 {
		t.Fatalf("full=%#v err=%v", full, err)
	}
	removed, err := store.ClearSucceeded(ctx)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	if cleared, err := store.Get(ctx, succeededID); err != nil || cleared != nil {
		t.Fatalf("cleared=%#v err=%v", cleared, err)
	}
	kept, err := store.Get(ctx, pendingID)
	if err != nil || kept == nil || kept.Status != StatusPending {
		t.Fatalf("pending=%#v err=%v", kept, err)
	}
	search, err := store.Search(ctx, SearchRequest{Keyword: "完整文字", Limit: 20})
	if err != nil || search.Total != 0 {
		t.Fatalf("search=%#v err=%v", search, err)
	}
}
