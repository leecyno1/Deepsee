package messagehook

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
	"github.com/sjzar/chatlog/internal/model"
)

const (
	outboxStatusPending    = "pending"
	outboxStatusDelivering = "delivering"
	outboxStatusRetry      = "retry"
	outboxStatusSent       = "sent"
	outboxStatusDead       = "dead"
	outboxStatusCanceled   = "canceled"
	maxDeliveryAttempts    = 12
)

type deliveryTarget struct {
	Name        string
	Destination string
}

type deliveryJob struct {
	EventID     string
	Target      string
	Destination string
	Event       Event
	Attempts    int
}

type hookActivation struct {
	Enabled         bool
	EnabledAt       int64
	RuleFingerprint string
}

type StoreStats = ports.HookStoreStats

type outboxStore struct {
	path string
	db   *sql.DB
}

func openOutboxStore(path string) (*outboxStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("hook store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &outboxStore{path: path, db: db}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrateLegacyEvents(filepath.Join(filepath.Dir(path), "hook_events.json")); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return store, nil
}

func (s *outboxStore) init() error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		// WAL + NORMAL keeps committed queue data crash-consistent while avoiding
		// a full fsync for every high-frequency cursor advance.
		`PRAGMA synchronous=NORMAL`,
		`CREATE TABLE IF NOT EXISTS hook_cursor (
			account TEXT NOT NULL,
			talker TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			local_id INTEGER NOT NULL,
			PRIMARY KEY(account, talker)
		)`,
		`CREATE TABLE IF NOT EXISTS hook_activation (
			account TEXT NOT NULL PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0,
			enabled_at INTEGER NOT NULL DEFAULT 0,
			rule_fingerprint TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS hook_outbox (
			event_id TEXT NOT NULL,
			target TEXT NOT NULL,
			destination TEXT NOT NULL DEFAULT '',
			payload BLOB NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_retry_at INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			visible INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(event_id, target)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_hook_outbox_ready
			ON hook_outbox(status, next_retry_at, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_hook_outbox_visible
			ON hook_outbox(visible, created_at DESC)`,
		// A process may have stopped after claiming a row but before recording
		// its result. Return such rows to the retry queue on startup.
		`UPDATE hook_outbox
		 SET status = 'retry', next_retry_at = 0, updated_at = unixepoch()
		 WHERE status = 'delivering'`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	// Existing hook.db files predate rule fingerprints. A blank fingerprint is
	// intentionally treated as a legacy epoch by the scanner, causing one
	// current-time watermark refresh instead of replaying historical rows.
	_, _ = s.db.Exec(`ALTER TABLE hook_activation ADD COLUMN rule_fingerprint TEXT NOT NULL DEFAULT ''`)
	return nil
}

func (s *outboxStore) migrateLegacyEvents(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var events []Event
	if err := json.Unmarshal(data, &events); err != nil {
		return fmt.Errorf("parse legacy hook events: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, event := range events {
		if strings.TrimSpace(event.EventID) == "" {
			event.EventID, event.ID = stableEventIdentity(event.OwnerWxid, event.Talker, event.TriggerSeq)
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		createdAt := time.Now().Unix()
		if parsed, err := time.Parse(time.RFC3339, event.CreatedAt); err == nil {
			createdAt = parsed.Unix()
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO hook_outbox(
				event_id, target, destination, payload, status,
				attempts, next_retry_at, last_error, visible, created_at, updated_at
			) VALUES(?, 'legacy', '', ?, ?, 1, 0, '', 1, ?, ?)
		`, event.EventID, payload, outboxStatusSent, createdAt, createdAt); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return os.Remove(path)
}

func (s *outboxStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *outboxStore) Cursor(account, talker string) (model.MessageCursor, bool, error) {
	var cursor model.MessageCursor
	err := s.db.QueryRow(
		`SELECT timestamp, local_id FROM hook_cursor WHERE account = ? AND talker = ?`,
		account, talker,
	).Scan(&cursor.Timestamp, &cursor.LocalID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.MessageCursor{}, false, nil
	}
	return cursor, err == nil, err
}

func (s *outboxStore) Activation(account string) (hookActivation, bool, error) {
	var state hookActivation
	var enabled int
	err := s.db.QueryRow(
		`SELECT enabled, enabled_at, rule_fingerprint FROM hook_activation WHERE account = ?`,
		account,
	).Scan(&enabled, &state.EnabledAt, &state.RuleFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return hookActivation{}, false, nil
	}
	if err != nil {
		return hookActivation{}, false, err
	}
	state.Enabled = enabled != 0
	return state, true, nil
}

func (s *outboxStore) SetInactive(account string, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO hook_activation(account, enabled, enabled_at, rule_fingerprint, updated_at)
		VALUES(?, 0, 0, '', ?)
		ON CONFLICT(account) DO UPDATE SET
			enabled = 0,
			enabled_at = 0,
			rule_fingerprint = '',
			updated_at = excluded.updated_at
	`, account, now.Unix())
	return err
}

// Activate atomically records the new real-time push epoch together with a
// snapshot of every conversation cursor. All cursors from the prior rule epoch
// are discarded first, including conversations absent from the current session
// list, so they can never replay rows from before enabledAt when they reappear.
func (s *outboxStore) Activate(account string, enabledAt time.Time, cursors map[string]model.MessageCursor, ruleFingerprint ...string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	fingerprint := ""
	if len(ruleFingerprint) > 0 {
		fingerprint = strings.TrimSpace(ruleFingerprint[0])
	}
	if _, err := tx.Exec(`DELETE FROM hook_cursor WHERE account = ?`, account); err != nil {
		return err
	}

	for talker, cursor := range cursors {
		if strings.TrimSpace(talker) == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO hook_cursor(account, talker, timestamp, local_id)
			VALUES(?, ?, ?, ?)
			ON CONFLICT(account, talker) DO UPDATE SET
				timestamp = excluded.timestamp,
				local_id = excluded.local_id
			WHERE excluded.timestamp > hook_cursor.timestamp
			   OR (excluded.timestamp = hook_cursor.timestamp AND excluded.local_id > hook_cursor.local_id)
		`, account, talker, cursor.Timestamp, cursor.LocalID); err != nil {
			return err
		}
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		INSERT INTO hook_activation(account, enabled, enabled_at, rule_fingerprint, updated_at)
		VALUES(?, 1, ?, ?, ?)
		ON CONFLICT(account) DO UPDATE SET
			enabled = 1,
			enabled_at = excluded.enabled_at,
			rule_fingerprint = excluded.rule_fingerprint,
			updated_at = excluded.updated_at
	`, account, enabledAt.Unix(), fingerprint, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *outboxStore) Commit(account, talker string, cursor model.MessageCursor, event *Event, targets []deliveryTarget) error {
	events := []Event(nil)
	if event != nil {
		events = append(events, *event)
	}
	return s.CommitEvents(account, talker, cursor, events, targets)
}

func (s *outboxStore) CommitEvents(account, talker string, cursor model.MessageCursor, events []Event, targets []deliveryTarget) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, event := range events {
		if err := enqueueEventTx(tx, event, targets); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`
		INSERT INTO hook_cursor(account, talker, timestamp, local_id)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(account, talker) DO UPDATE SET
			timestamp = excluded.timestamp,
			local_id = excluded.local_id
		WHERE excluded.timestamp > hook_cursor.timestamp
		   OR (excluded.timestamp = hook_cursor.timestamp AND excluded.local_id > hook_cursor.local_id)
	`, account, talker, cursor.Timestamp, cursor.LocalID); err != nil {
		return err
	}
	return tx.Commit()
}

func enqueueEventTx(tx *sql.Tx, event Event, targets []deliveryTarget) error {
	event.Deliveries = nil
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, target := range targets {
		if strings.TrimSpace(target.Name) == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO hook_outbox(
				event_id, target, destination, payload, status,
				attempts, next_retry_at, last_error, visible, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, 0, 0, '', 1, ?, ?)
		`, event.EventID, target.Name, target.Destination, payload, outboxStatusPending, now, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *outboxStore) EnqueueEvent(event Event, targets []deliveryTarget) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := enqueueEventTx(tx, event, targets); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *outboxStore) Claim(now time.Time) (*deliveryJob, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var job deliveryJob
	var payload []byte
	err = tx.QueryRow(`
		SELECT event_id, target, destination, payload, attempts
		FROM hook_outbox
		WHERE status IN (?, ?) AND next_retry_at <= ?
		ORDER BY created_at ASC, event_id ASC, target ASC
		LIMIT 1
	`, outboxStatusPending, outboxStatusRetry, now.Unix()).Scan(
		&job.EventID, &job.Target, &job.Destination, &payload, &job.Attempts,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.Exec(`
		UPDATE hook_outbox SET status = ?, updated_at = ?
		WHERE event_id = ? AND target = ? AND status IN (?, ?)
	`, outboxStatusDelivering, now.Unix(), job.EventID, job.Target, outboxStatusPending, outboxStatusRetry)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, tx.Commit()
	}
	if err := json.Unmarshal(payload, &job.Event); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

// NextDeliveryAt returns the exact next durable delivery deadline. Workers can
// sleep until this timestamp or an enqueue wake instead of polling SQLite.
func (s *outboxStore) NextDeliveryAt() (time.Time, bool, error) {
	var next sql.NullInt64
	err := s.db.QueryRow(`
		SELECT MIN(next_retry_at)
		FROM hook_outbox
		WHERE status IN (?, ?)
	`, outboxStatusPending, outboxStatusRetry).Scan(&next)
	if err != nil {
		return time.Time{}, false, err
	}
	if !next.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(next.Int64, 0), true, nil
}

func (s *outboxStore) Complete(job deliveryJob, result DeliveryResult, now time.Time) error {
	attempts := job.Attempts + 1
	status := outboxStatusSent
	nextRetryAt := int64(0)
	lastError := ""
	if !result.Success {
		lastError = strings.TrimSpace(result.Detail)
		if attempts >= maxDeliveryAttempts {
			status = outboxStatusDead
		} else {
			status = outboxStatusRetry
			nextRetryAt = now.Add(postRetryDelay(attempts - 1)).Unix()
		}
	}
	_, err := s.db.Exec(`
		UPDATE hook_outbox
		SET status = ?, attempts = ?, next_retry_at = ?, last_error = ?, updated_at = ?
		WHERE event_id = ? AND target = ? AND status = ?
	`, status, attempts, nextRetryAt, lastError, now.Unix(), job.EventID, job.Target, outboxStatusDelivering)
	return err
}

func (s *outboxStore) CancelEvent(eventID, target string, now time.Time) (int64, error) {
	eventID = strings.TrimSpace(eventID)
	target = strings.TrimSpace(target)
	if eventID == "" {
		return 0, fmt.Errorf("event id is empty")
	}
	query := `
		UPDATE hook_outbox
		SET status = ?, next_retry_at = 0, last_error = 'canceled by user', updated_at = ?
		WHERE event_id = ? AND status IN (?, ?, ?)`
	args := []interface{}{
		outboxStatusCanceled, now.Unix(), eventID,
		outboxStatusPending, outboxStatusRetry, outboxStatusDelivering,
	}
	if target != "" {
		query += ` AND target = ?`
		args = append(args, target)
	}
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *outboxStore) RetryEvent(eventID, target string, now time.Time) (int64, error) {
	eventID = strings.TrimSpace(eventID)
	target = strings.TrimSpace(target)
	if eventID == "" {
		return 0, fmt.Errorf("event id is empty")
	}
	query := `
		UPDATE hook_outbox
		SET status = ?, attempts = 0, next_retry_at = 0, last_error = '', visible = 1, updated_at = ?
		WHERE event_id = ? AND status IN (?, ?, ?)`
	args := []interface{}{
		outboxStatusPending, now.Unix(), eventID,
		outboxStatusRetry, outboxStatusDead, outboxStatusCanceled,
	}
	if target != "" {
		query += ` AND target = ?`
		args = append(args, target)
	}
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func normalizeBatchEventIDs(eventIDs []string) ([]string, error) {
	if len(eventIDs) == 0 {
		return nil, fmt.Errorf("event ids are empty")
	}
	if len(eventIDs) > 200 {
		return nil, fmt.Errorf("too many event ids: %d", len(eventIDs))
	}
	seen := make(map[string]struct{}, len(eventIDs))
	result := make([]string, 0, len(eventIDs))
	for _, eventID := range eventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID == "" {
			continue
		}
		if _, exists := seen[eventID]; exists {
			continue
		}
		seen[eventID] = struct{}{}
		result = append(result, eventID)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("event ids are empty")
	}
	return result, nil
}

func (s *outboxStore) RetryEvents(eventIDs []string, target string, now time.Time) (int64, error) {
	eventIDs, err := normalizeBatchEventIDs(eventIDs)
	if err != nil {
		return 0, err
	}
	target = strings.TrimSpace(target)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var affected int64
	for _, eventID := range eventIDs {
		query := `
			UPDATE hook_outbox
			SET status = ?, attempts = 0, next_retry_at = 0, last_error = '', visible = 1, updated_at = ?
			WHERE event_id = ? AND status IN (?, ?, ?)`
		args := []interface{}{outboxStatusPending, now.Unix(), eventID, outboxStatusRetry, outboxStatusDead, outboxStatusCanceled}
		if target != "" {
			query += ` AND target = ?`
			args = append(args, target)
		}
		result, execErr := tx.Exec(query, args...)
		if execErr != nil {
			return 0, execErr
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return 0, rowsErr
		}
		affected += rows
	}
	return affected, tx.Commit()
}

func (s *outboxStore) DeleteEvent(eventID, target string) (int64, error) {
	eventID = strings.TrimSpace(eventID)
	target = strings.TrimSpace(target)
	if eventID == "" {
		return 0, fmt.Errorf("event id is empty")
	}
	query := `DELETE FROM hook_outbox WHERE event_id = ?`
	args := []interface{}{eventID}
	if target != "" {
		query += ` AND target = ?`
		args = append(args, target)
	}
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *outboxStore) DeleteEvents(eventIDs []string, target string) (int64, error) {
	eventIDs, err := normalizeBatchEventIDs(eventIDs)
	if err != nil {
		return 0, err
	}
	target = strings.TrimSpace(target)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var affected int64
	for _, eventID := range eventIDs {
		query := `DELETE FROM hook_outbox WHERE event_id = ?`
		args := []interface{}{eventID}
		if target != "" {
			query += ` AND target = ?`
			args = append(args, target)
		}
		result, execErr := tx.Exec(query, args...)
		if execErr != nil {
			return 0, execErr
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return 0, rowsErr
		}
		affected += rows
	}
	return affected, tx.Commit()
}

func (s *outboxStore) RecentEvents(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT event_id, payload
		FROM hook_outbox
		WHERE visible = 1
		GROUP BY event_id
		ORDER BY MAX(created_at) DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	type storedEvent struct {
		eventID string
		payload []byte
	}
	stored := make([]storedEvent, 0, limit)
	for rows.Next() {
		var eventID string
		var payload []byte
		if err := rows.Scan(&eventID, &payload); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stored = append(stored, storedEvent{eventID: eventID, payload: append([]byte(nil), payload...)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	events := make([]Event, 0, len(stored))
	for _, item := range stored {
		var event Event
		if err := json.Unmarshal(item.payload, &event); err != nil {
			return nil, err
		}
		deliveryRows, err := s.db.Query(`
			SELECT target, status, last_error, attempts, next_retry_at
			FROM hook_outbox
			WHERE event_id = ?
			ORDER BY target
		`, item.eventID)
		if err != nil {
			return nil, err
		}
		for deliveryRows.Next() {
			var target, status, detail string
			var attempts int
			var nextRetryAt int64
			if err := deliveryRows.Scan(&target, &status, &detail, &attempts, &nextRetryAt); err != nil {
				_ = deliveryRows.Close()
				return nil, err
			}
			nextRetry := ""
			if nextRetryAt > 0 {
				nextRetry = time.Unix(nextRetryAt, 0).Format(time.RFC3339)
			}
			event.Deliveries = append(event.Deliveries, DeliveryResult{
				Target:      target,
				Status:      status,
				Detail:      detail,
				Success:     status == outboxStatusSent,
				Attempts:    attempts,
				NextRetryAt: nextRetry,
			})
		}
		if err := deliveryRows.Close(); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *outboxStore) ClearEvents() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT event_id) FROM hook_outbox WHERE visible = 1`).Scan(&count); err != nil {
		return 0, err
	}
	_, err := s.db.Exec(`UPDATE hook_outbox SET visible = 0 WHERE visible = 1`)
	return count, err
}

func (s *outboxStore) Stats() (StoreStats, error) {
	var stats StoreStats
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT event_id) FROM hook_outbox WHERE visible = 1`).Scan(&stats.EventCount); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM hook_outbox WHERE status IN (?, ?, ?)`,
		outboxStatusPending, outboxStatusRetry, outboxStatusDelivering).Scan(&stats.PendingDeliveries); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM hook_outbox WHERE status = ?`, outboxStatusDead).Scan(&stats.FailedDeliveries); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM hook_cursor`).Scan(&stats.CursorCount); err != nil {
		return stats, err
	}
	var last sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(created_at) FROM hook_outbox`).Scan(&last); err != nil {
		return stats, err
	}
	if last.Valid {
		stats.LastEventAt = time.Unix(last.Int64, 0).Format(time.RFC3339)
	}
	return stats, nil
}

func (s *outboxStore) Prune(now time.Time) error {
	_, err := s.db.Exec(`
		DELETE FROM hook_outbox
		WHERE status IN (?, ?)
		  AND ((visible = 0 AND updated_at < ?) OR updated_at < ?)
	`, outboxStatusSent, outboxStatusDead, now.Add(-24*time.Hour).Unix(), now.Add(-30*24*time.Hour).Unix())
	return err
}

func (s *outboxStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
