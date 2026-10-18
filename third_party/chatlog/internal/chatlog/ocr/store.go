package ocr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

const (
	StatusWaitingMedia = "waiting_media"
	StatusPending      = "pending"
	StatusProcessing   = "processing"
	StatusSucceeded    = "succeeded"
	StatusFailed       = "failed"
)

// ImageRef is the stable message/media identity stored outside WeChat's
// read-only databases.
type ImageRef struct {
	Talker      string
	TalkerName  string
	Sender      string
	SenderName  string
	IsSelf      bool
	MessageTime int64
	MessageSeq  int64
	DBLocalID   int64
	MessageID   int64
	MediaKey    string
	MediaPath   string
	// CDN metadata from image XML — used to request mid/HD download when only
	// a thumbnail is present on disk.
	AesKey       string
	CdnMidImgURL string
	CdnBigImgURL string
	CdnThumbURL  string
	HdLength     string
	Length       string
}

type Record struct {
	ID                 int64 `json:"id"`
	ImageRef           `json:",inline"`
	ContentHash        string          `json:"content_hash,omitempty"`
	Provider           string          `json:"provider,omitempty"`
	Model              string          `json:"model,omitempty"`
	Description        string          `json:"description,omitempty"`
	OCRText            string          `json:"ocr_text,omitempty"`
	Markdown           string          `json:"markdown,omitempty"`
	Layout             json.RawMessage `json:"layout,omitempty"`
	Status             string          `json:"status"`
	Error              string          `json:"error,omitempty"`
	Attempts           int             `json:"attempts"`
	NextAttemptAt      int64           `json:"next_attempt_at,omitempty"`
	MediaRetryAttempts int             `json:"media_retry_attempts,omitempty"`
	MediaNextRetryAt   int64           `json:"media_next_retry_at,omitempty"`
	MediaLastRetryAt   int64           `json:"media_last_retry_at,omitempty"`
	MediaRetryError    string          `json:"media_retry_error,omitempty"`
	CreatedAt          int64           `json:"created_at"`
	UpdatedAt          int64           `json:"updated_at"`
	Rank               float64         `json:"rank,omitempty"`
}

type SearchRequest struct {
	Keyword string
	Talkers []string
	Since   int64
	Until   int64
	Limit   int
	Offset  int
}

type SearchResult struct {
	Records []Record `json:"records"`
	Total   int      `json:"total"`
	Path    string   `json:"path"`
}

type Stats struct {
	Path         string `json:"path"`
	FTSEnabled   bool   `json:"fts_enabled"`
	IndexPath    string `json:"index_path"`
	Total        int64  `json:"total"`
	WaitingMedia int64  `json:"waiting_media"`
	Pending      int64  `json:"pending"`
	Processing   int64  `json:"processing"`
	Succeeded    int64  `json:"succeeded"`
	Failed       int64  `json:"failed"`
	Searchable   int64  `json:"searchable"`
	LastUpdated  int64  `json:"last_updated"`
}

type RecordSummary struct {
	ID                 int64  `json:"id"`
	Talker             string `json:"talker"`
	TalkerName         string `json:"talker_name,omitempty"`
	SenderName         string `json:"sender_name,omitempty"`
	MessageTime        int64  `json:"message_time"`
	MediaKey           string `json:"media_key,omitempty"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	Description        string `json:"description,omitempty"`
	Status             string `json:"status"`
	Error              string `json:"error,omitempty"`
	Attempts           int    `json:"attempts"`
	NextAttemptAt      int64  `json:"next_attempt_at,omitempty"`
	MediaRetryAttempts int    `json:"media_retry_attempts,omitempty"`
	MediaNextRetryAt   int64  `json:"media_next_retry_at,omitempty"`
	MediaLastRetryAt   int64  `json:"media_last_retry_at,omitempty"`
	MediaRetryError    string `json:"media_retry_error,omitempty"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type Store struct {
	path       string
	db         *sql.DB
	ftsEnabled bool
	mu         sync.RWMutex
}

func OpenStore(path string) (*Store, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return nil, errors.New("OCR index path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create OCR index directory: %w", err)
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open OCR index: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{path: path, db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0600)
	return store, nil
}

func (s *Store) init() error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`CREATE TABLE IF NOT EXISTS image_ocr (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			talker TEXT NOT NULL,
			talker_name TEXT NOT NULL DEFAULT '',
			sender TEXT NOT NULL DEFAULT '',
			sender_name TEXT NOT NULL DEFAULT '',
			is_self INTEGER NOT NULL DEFAULT 0,
			message_time INTEGER NOT NULL,
			message_seq INTEGER NOT NULL DEFAULT 0,
			db_local_id INTEGER NOT NULL DEFAULT 0,
			message_id INTEGER NOT NULL DEFAULT 0,
			media_key TEXT NOT NULL DEFAULT '',
			media_path TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			ocr_text TEXT NOT NULL DEFAULT '',
			markdown TEXT NOT NULL DEFAULT '',
			layout_json TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			error TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(talker, message_time, db_local_id, media_key, media_path)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_image_ocr_status_due ON image_ocr(status, next_attempt_at, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_image_ocr_message ON image_ocr(talker, message_time, db_local_id)`,
		`CREATE INDEX IF NOT EXISTS idx_image_ocr_media ON image_ocr(media_key, media_path)`,
		`CREATE TABLE IF NOT EXISTS image_ocr_cursor (
			talker TEXT PRIMARY KEY,
			message_time INTEGER NOT NULL DEFAULT 0,
			db_local_id INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("initialize OCR index: %w", err)
		}
	}
	// Optional CDN metadata for native original-image download (best-effort migrate).
	for _, col := range []string{
		`ALTER TABLE image_ocr ADD COLUMN aes_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE image_ocr ADD COLUMN cdn_mid_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE image_ocr ADD COLUMN cdn_big_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE image_ocr ADD COLUMN cdn_thumb_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE image_ocr ADD COLUMN media_retry_attempts INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE image_ocr ADD COLUMN media_next_retry_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE image_ocr ADD COLUMN media_last_retry_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE image_ocr ADD COLUMN media_retry_error TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = s.db.Exec(col)
	}
	// Trigram FTS5 provides true substring matching for unsegmented Chinese.
	// Builds without SQLite FTS5 keep full functionality through the indexed
	// metadata table plus a literal LIKE fallback.
	if _, err := s.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS image_ocr_fts
		USING fts5(record_id UNINDEXED, description, ocr_text, tokenize='trigram')`); err == nil {
		s.ftsEnabled = true
		if _, err := s.db.Exec(`INSERT INTO image_ocr_fts(record_id, description, ocr_text)
			SELECT CAST(id AS TEXT), description, ocr_text FROM image_ocr
			WHERE status = 'succeeded'
			  AND NOT EXISTS (
				SELECT 1 FROM image_ocr_fts f WHERE CAST(f.record_id AS INTEGER) = image_ocr.id
			  )`); err != nil {
			return fmt.Errorf("hydrate OCR FTS index: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) FTSEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ftsEnabled
}

func normalizeRef(ref ImageRef) ImageRef {
	ref.Talker = strings.TrimSpace(ref.Talker)
	ref.TalkerName = strings.TrimSpace(ref.TalkerName)
	ref.Sender = strings.TrimSpace(ref.Sender)
	ref.SenderName = strings.TrimSpace(ref.SenderName)
	ref.MediaKey = strings.TrimSpace(ref.MediaKey)
	ref.MediaPath = filepath.ToSlash(strings.TrimSpace(ref.MediaPath))
	ref.AesKey = strings.TrimSpace(ref.AesKey)
	ref.CdnMidImgURL = strings.TrimSpace(ref.CdnMidImgURL)
	ref.CdnBigImgURL = strings.TrimSpace(ref.CdnBigImgURL)
	ref.CdnThumbURL = strings.TrimSpace(ref.CdnThumbURL)
	ref.HdLength = strings.TrimSpace(ref.HdLength)
	ref.Length = strings.TrimSpace(ref.Length)
	return ref
}

func (s *Store) Enqueue(ctx context.Context, ref ImageRef) (int64, bool, error) {
	return s.EnqueueWithStatus(ctx, ref, StatusPending)
}

// EnqueueWithStatus stores a ready OCR task or a durable media waiter.
func (s *Store) EnqueueWithStatus(ctx context.Context, ref ImageRef, status string) (int64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, errors.New("OCR index is closed")
	}
	if status != StatusPending && status != StatusWaitingMedia {
		return 0, false, fmt.Errorf("invalid OCR enqueue status: %s", status)
	}
	ref = normalizeRef(ref)
	if ref.Talker == "" || ref.MessageTime <= 0 || (ref.MediaKey == "" && ref.MediaPath == "") {
		return 0, false, errors.New("incomplete OCR image reference")
	}
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, enqueueImageSQL,
		ref.Talker, ref.TalkerName, ref.Sender, ref.SenderName, boolInt(ref.IsSelf),
		ref.MessageTime, ref.MessageSeq, ref.DBLocalID, ref.MessageID, ref.MediaKey,
		ref.MediaPath, ref.AesKey, ref.CdnMidImgURL, ref.CdnBigImgURL, ref.CdnThumbURL,
		status, now, now,
	)
	if err != nil {
		return 0, false, fmt.Errorf("enqueue OCR image: %w", err)
	}
	affected, _ := result.RowsAffected()
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM image_ocr
		WHERE talker = ? AND message_time = ? AND db_local_id = ?
		  AND media_key = ? AND media_path = ?`,
		ref.Talker, ref.MessageTime, ref.DBLocalID, ref.MediaKey, ref.MediaPath,
	).Scan(&id)
	if err == nil && affected == 0 && (ref.AesKey != "" || ref.CdnMidImgURL != "" || ref.CdnBigImgURL != "") {
		// Refresh CDN metadata on existing pending rows so download trigger can run.
		_, _ = s.db.ExecContext(ctx, `UPDATE image_ocr
			SET aes_key = CASE WHEN aes_key = '' THEN ? ELSE aes_key END,
			    cdn_mid_url = CASE WHEN cdn_mid_url = '' THEN ? ELSE cdn_mid_url END,
			    cdn_big_url = CASE WHEN cdn_big_url = '' THEN ? ELSE cdn_big_url END,
			    cdn_thumb_url = CASE WHEN cdn_thumb_url = '' THEN ? ELSE cdn_thumb_url END,
			    updated_at = ?
			WHERE id = ?`,
			ref.AesKey, ref.CdnMidImgURL, ref.CdnBigImgURL, ref.CdnThumbURL, now, id,
		)
	}
	if err == nil && affected == 0 && status == StatusPending {
		promoted, promoteErr := s.db.ExecContext(ctx, `UPDATE image_ocr SET
			status = 'pending', error = '', next_attempt_at = 0, updated_at = ?
			WHERE id = ? AND status = 'waiting_media'`, now, id)
		if promoteErr != nil {
			return id, false, promoteErr
		}
		affected, _ = promoted.RowsAffected()
	}
	return id, affected > 0, err
}

func (s *Store) Cursor(ctx context.Context, talker string) (messageTime, localID int64, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT message_time, db_local_id FROM image_ocr_cursor WHERE talker = ?`,
		strings.TrimSpace(talker)).Scan(&messageTime, &localID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	return messageTime, localID, err == nil, err
}

func (s *Store) SetCursor(ctx context.Context, talker string, messageTime, localID int64) error {
	_, err := s.db.ExecContext(ctx, advanceOCRCursorSQL,
		strings.TrimSpace(talker), messageTime, localID, time.Now().Unix(),
	)
	return err
}

func (s *Store) NextPending(ctx context.Context) (*Record, error) {
	now := time.Now().Unix()
	row := s.db.QueryRowContext(ctx, recordSelect+`
		WHERE (status = 'pending')
		   OR (status = 'failed' AND next_attempt_at <= ?)
		   OR (status = 'processing' AND updated_at <= ?)
		ORDER BY CASE status WHEN 'pending' THEN 0 WHEN 'failed' THEN 1 ELSE 2 END,
		         next_attempt_at ASC, message_time ASC, id ASC
		LIMIT 1`, now, now-600)
	record, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

// NextWorkAt returns the exact next time at which a pending, failed-retry, or
// abandoned-processing record becomes runnable. OCR workers use it to sleep on
// a timer or explicit enqueue wake rather than polling the index.
func (s *Store) NextWorkAt(ctx context.Context) (time.Time, bool, error) {
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MIN(CASE
			WHEN status = 'pending' THEN 0
			WHEN status = 'failed' THEN next_attempt_at
			WHEN status = 'processing' THEN updated_at + 600
		END)
		FROM image_ocr
		WHERE status IN ('pending', 'failed', 'processing')
	`).Scan(&next)
	if err != nil {
		return time.Time{}, false, err
	}
	if !next.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(next.Int64, 0), true, nil
}

func (s *Store) MarkProcessing(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE image_ocr SET
		status = 'processing', attempts = attempts + 1, error = '', updated_at = ?
		WHERE id = ?`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) MarkSucceeded(
	ctx context.Context,
	id int64,
	contentHash, provider, model, description, ocrText, markdown string,
	layout json.RawMessage,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE image_ocr SET
		content_hash = ?, provider = ?, model = ?, description = ?, ocr_text = ?,
		markdown = ?, layout_json = ?, status = 'succeeded', error = '',
		next_attempt_at = 0, updated_at = ?
		WHERE id = ?`,
		strings.TrimSpace(contentHash), strings.TrimSpace(provider), strings.TrimSpace(model),
		strings.TrimSpace(description), strings.TrimSpace(ocrText), strings.TrimSpace(markdown),
		string(layout), now, id,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	if s.FTSEnabled() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM image_ocr_fts WHERE record_id = ?`, strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO image_ocr_fts(record_id, description, ocr_text)
			VALUES (?, ?, ?)`, strconv.FormatInt(id, 10), description, ocrText); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MarkFailed(ctx context.Context, id int64, cause error) error {
	var attempts int
	_ = s.db.QueryRowContext(ctx, `SELECT attempts FROM image_ocr WHERE id = ?`, id).Scan(&attempts)
	delay := retryDelay(attempts)
	message := "OCR request failed"
	if cause != nil {
		message = strings.TrimSpace(cause.Error())
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE image_ocr SET
		status = 'failed', error = ?, next_attempt_at = ?, updated_at = ?
		WHERE id = ?`, message, time.Now().Add(delay).Unix(), time.Now().Unix(), id)
	return err
}

func retryDelay(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 15 * time.Second
	case attempts == 2:
		return time.Minute
	case attempts == 3:
		return 5 * time.Minute
	case attempts == 4:
		return 30 * time.Minute
	default:
		return 2 * time.Hour
	}
}

func (s *Store) Retry(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE image_ocr SET status = 'pending', error = '',
		next_attempt_at = 0, updated_at = ? WHERE id = ? AND status <> 'succeeded'`,
		time.Now().Unix(), id)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

// RetryConnectionFailures requeues tasks that failed only because the managed
// local OCR HTTP service was not listening yet.
func (s *Store) RetryConnectionFailures(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE image_ocr SET
		status = 'pending', error = '', next_attempt_at = 0, updated_at = ?
		WHERE status = 'failed'
		  AND (
		    LOWER(error) LIKE '%connection refused%'
		    OR LOWER(error) LIKE '%connect: cannot assign requested address%'
		  )`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

// SyncRetryableTarget makes every unfinished task use the newly selected OCR
// route. Pending work keeps its place, failed work becomes immediately due, and
// interrupted processing work is recovered instead of waiting for the stale
// processing timeout. Successful OCR results remain immutable.
func (s *Store) SyncRetryableTarget(ctx context.Context, provider, model string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("OCR index is closed")
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		return 0, errors.New("OCR retry provider is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE image_ocr SET
		provider = ?, model = ?, status = 'pending', error = '',
		next_attempt_at = 0, updated_at = ?
		WHERE status IN ('pending', 'failed', 'processing')`,
		provider, model, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return affected, nil
}

func (s *Store) Find(ctx context.Context, ref ImageRef) (*Record, error) {
	ref = normalizeRef(ref)
	args := []any{ref.Talker, ref.MessageTime, ref.DBLocalID}
	query := recordSelect + ` WHERE talker = ? AND message_time = ? AND db_local_id = ?`
	if ref.MediaKey != "" {
		query += ` AND media_key = ?`
		args = append(args, ref.MediaKey)
	}
	query += ` ORDER BY CASE status WHEN 'succeeded' THEN 0 ELSE 1 END, updated_at DESC LIMIT 1`
	record, err := scanRecord(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func (s *Store) Search(ctx context.Context, request SearchRequest) (SearchResult, error) {
	keyword := strings.TrimSpace(request.Keyword)
	if keyword == "" {
		return SearchResult{}, errors.New("OCR search keyword is required")
	}
	if request.Limit <= 0 {
		request.Limit = 20
	}
	if request.Limit > 5000 {
		request.Limit = 5000
	}
	if request.Offset < 0 {
		request.Offset = 0
	}

	useFTS := s.FTSEnabled() && utf8.RuneCountInString(keyword) >= 3
	path := "image_ocr_literal"
	from := ` FROM image_ocr r`
	where := []string{`r.status = 'succeeded'`}
	args := make([]any, 0, 8)
	if useFTS {
		path = "image_ocr_fts5_trigram"
		from += ` JOIN image_ocr_fts f ON CAST(f.record_id AS INTEGER) = r.id`
		where = append(where, `image_ocr_fts MATCH ?`)
		args = append(args, quoteFTSLiteral(keyword))
	} else {
		where = append(where, `(r.description LIKE ? ESCAPE '\' OR r.ocr_text LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(keyword) + "%"
		args = append(args, pattern, pattern)
	}
	if len(request.Talkers) > 0 {
		holders := make([]string, 0, len(request.Talkers))
		for _, talker := range request.Talkers {
			talker = strings.TrimSpace(talker)
			if talker == "" {
				continue
			}
			holders = append(holders, "?")
			args = append(args, talker)
		}
		if len(holders) > 0 {
			where = append(where, `r.talker IN (`+strings.Join(holders, ",")+`)`)
		}
	}
	if request.Since > 0 {
		where = append(where, `r.message_time >= ?`)
		args = append(args, request.Since)
	}
	if request.Until > 0 {
		where = append(where, `r.message_time <= ?`)
		args = append(args, request.Until)
	}
	whereSQL := " WHERE " + strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+from+whereSQL, args...).Scan(&total); err != nil {
		return SearchResult{}, fmt.Errorf("count OCR search results: %w", err)
	}
	selectSQL := recordSelectColumns("r")
	orderSQL := ` ORDER BY r.message_time DESC, r.db_local_id DESC, r.id DESC`
	if useFTS {
		selectSQL += `, bm25(image_ocr_fts) AS search_rank`
		orderSQL = ` ORDER BY search_rank ASC, r.message_time DESC, r.id DESC`
	} else {
		selectSQL += `, 0.0 AS search_rank`
	}
	queryArgs := append(append([]any(nil), args...), request.Limit, request.Offset)
	rows, err := s.db.QueryContext(ctx, selectSQL+from+whereSQL+orderSQL+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return SearchResult{}, fmt.Errorf("query OCR search results: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0, request.Limit)
	for rows.Next() {
		record, scanErr := scanRecordWithRank(rows)
		if scanErr != nil {
			return SearchResult{}, scanErr
		}
		records = append(records, *record)
	}
	if err := rows.Err(); err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Records: records, Total: total, Path: path}, nil
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{
		Path:       "image_ocr_literal",
		FTSEnabled: s.FTSEnabled(),
		IndexPath:  s.Path(),
	}
	if stats.FTSEnabled {
		stats.Path = "image_ocr_fts5_trigram"
	}
	err := s.db.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN status = 'waiting_media' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'processing' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'succeeded' AND (ocr_text <> '' OR description <> '') THEN 1 ELSE 0 END), 0),
		COALESCE(MAX(updated_at), 0)
		FROM image_ocr`).Scan(
		&stats.Total, &stats.WaitingMedia, &stats.Pending, &stats.Processing, &stats.Succeeded,
		&stats.Failed, &stats.Searchable, &stats.LastUpdated,
	)
	return stats, err
}

func (s *Store) Recent(ctx context.Context, limit int) ([]RecordSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, recordSelect+` ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RecordSummary, 0, limit)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, RecordSummary{
			ID:                 record.ID,
			Talker:             record.Talker,
			TalkerName:         record.TalkerName,
			SenderName:         record.SenderName,
			MessageTime:        record.MessageTime,
			MediaKey:           record.MediaKey,
			Provider:           record.Provider,
			Model:              record.Model,
			Description:        record.Description,
			Status:             record.Status,
			Error:              record.Error,
			Attempts:           record.Attempts,
			NextAttemptAt:      record.NextAttemptAt,
			MediaRetryAttempts: record.MediaRetryAttempts,
			MediaNextRetryAt:   record.MediaNextRetryAt,
			MediaLastRetryAt:   record.MediaLastRetryAt,
			MediaRetryError:    record.MediaRetryError,
			CreatedAt:          record.CreatedAt,
			UpdatedAt:          record.UpdatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func quoteFTSLiteral(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
