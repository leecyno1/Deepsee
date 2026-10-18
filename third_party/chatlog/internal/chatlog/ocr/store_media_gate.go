package ocr

import (
	"context"
	"errors"
	"time"
)

// MediaGateRecords returns unfinished records whose local image quality must be
// checked before an OCR request is allowed. Completed records are never
// reconsidered by this gate.
func (s *Store) MediaGateRecords(ctx context.Context, limit int) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("OCR index is closed")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, recordSelect+`
		WHERE status IN ('waiting_media', 'pending', 'failed')
		ORDER BY CASE status WHEN 'waiting_media' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END,
		         updated_at ASC, id ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0, limit)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

// WaitingMediaRecords returns thumbnail-only records for manual batch retry.
func (s *Store) WaitingMediaRecords(ctx context.Context, limit int) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("OCR index is closed")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, recordSelect+`
		WHERE status = 'waiting_media'
		ORDER BY updated_at ASC, id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0, limit)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

// DueMediaRetryRecords returns only waiters whose passive receive-hook
// allowlist should be refreshed now. The bounded retry count prevents idle
// records from causing permanent periodic writes.
func (s *Store) DueMediaRetryRecords(ctx context.Context, now int64, maxAttempts, limit int) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("OCR index is closed")
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, recordSelect+`
		WHERE status = 'waiting_media'
		  AND media_retry_attempts < ?
		  AND media_next_retry_at <= ?
		ORDER BY media_next_retry_at ASC, updated_at ASC, id ASC LIMIT ?`, maxAttempts, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0, limit)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

func (s *Store) NextMediaRetryAt(ctx context.Context, maxAttempts int) (time.Time, bool, error) {
	if s == nil || s.db == nil {
		return time.Time{}, false, errors.New("OCR index is closed")
	}
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var next int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(media_next_retry_at), -1)
		FROM image_ocr WHERE status = 'waiting_media' AND media_retry_attempts < ?`, maxAttempts).Scan(&next)
	if err != nil {
		return time.Time{}, false, err
	}
	if next < 0 {
		return time.Time{}, false, nil
	}
	return time.Unix(next, 0), true, nil
}

// MarkMediaRetry records an allowlist refresh attempt separately from OCR
// request attempts. detail is shown in the queue without moving the record
// into the OCR worker before a mid/original file actually exists.
func (s *Store) MarkMediaRetry(ctx context.Context, id, nextRetryAt int64, detail string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("OCR index is closed")
	}
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE image_ocr SET
		media_retry_attempts = media_retry_attempts + 1,
		media_last_retry_at = ?, media_next_retry_at = ?, media_retry_error = ?, updated_at = ?
		WHERE id = ? AND status = 'waiting_media'`, now, nextRetryAt, detail, now, id)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

// MarkWaitingMedia removes a thumbnail-only record from the OCR work queue.
// It remains durable so a later mid/big/HD file can promote it without losing
// the message cursor across restarts.
func (s *Store) MarkWaitingMedia(ctx context.Context, id int64, detail string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("OCR index is closed")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE image_ocr SET
		status = 'waiting_media', error = ?, next_attempt_at = 0,
		media_next_retry_at = 0, updated_at = ?
		WHERE id = ? AND status IN ('pending', 'failed')`, detail, time.Now().Unix(), id)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

// MarkMediaReady promotes only a media waiter. Existing failed OCR requests
// keep their retry schedule; the gate is concerned solely with file quality.
func (s *Store) MarkMediaReady(ctx context.Context, id int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("OCR index is closed")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE image_ocr SET
		status = 'pending', error = '', next_attempt_at = 0,
		media_next_retry_at = 0, media_retry_error = '', updated_at = ?
		WHERE id = ? AND status = 'waiting_media'`, time.Now().Unix(), id)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}
