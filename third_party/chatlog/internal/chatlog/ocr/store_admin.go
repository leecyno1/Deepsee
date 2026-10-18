package ocr

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Reprocess turns a completed OCR record back into a clean pending task. The
// old searchable text is removed in the same transaction, so search never
// serves stale recognition output while the replacement request is pending.
func (s *Store) Reprocess(ctx context.Context, id int64, provider, model string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("OCR index is closed")
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		return false, errors.New("OCR reprocess provider is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE image_ocr SET
		content_hash = '', provider = ?, model = ?, description = '', ocr_text = '',
		markdown = '', layout_json = '', status = 'pending', error = '', attempts = 0,
		next_attempt_at = 0, updated_at = ?
		WHERE id = ? AND status = 'succeeded'`,
		provider, model, time.Now().Unix(), id,
	)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return false, nil
	}
	if s.FTSEnabled() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM image_ocr_fts WHERE record_id = ?`, strconv.FormatInt(id, 10)); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) Get(ctx context.Context, id int64) (*Record, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("OCR index is closed")
	}
	if id <= 0 {
		return nil, sql.ErrNoRows
	}
	record, err := scanRecord(s.db.QueryRowContext(ctx, recordSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

// ClearSucceeded removes completed OCR records and their searchable text while
// preserving pending, processing, and failed work. The image files themselves
// and WeChat's source databases remain untouched.
func (s *Store) ClearSucceeded(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("OCR index is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if s.FTSEnabled() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM image_ocr_fts
			WHERE CAST(record_id AS INTEGER) IN (
				SELECT id FROM image_ocr WHERE status = 'succeeded'
			)`); err != nil {
			return 0, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM image_ocr WHERE status = 'succeeded'`)
	if err != nil {
		return 0, err
	}
	removed, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}
