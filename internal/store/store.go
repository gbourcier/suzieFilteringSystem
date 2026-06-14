package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS emails (
  id            INTEGER PRIMARY KEY,
  uid_validity  INTEGER NOT NULL,
  imap_uid      INTEGER NOT NULL,
  message_id    TEXT,
  received_at   TEXT NOT NULL,
  from_name     TEXT,
  from_addr     TEXT,
  subject       TEXT,
  raw_path      TEXT NOT NULL,
  summary       TEXT,
  action_req    TEXT,
  deadline      TEXT,
  tone          TEXT,
  in_scope      INTEGER DEFAULT 1,
  llm_status    TEXT DEFAULT 'ok',
  llm_error     TEXT,
  processed_at  TEXT,
  digested_at   TEXT,
  UNIQUE(uid_validity, imap_uid),
  UNIQUE(message_id)
);

CREATE TABLE IF NOT EXISTS state (
  key   TEXT PRIMARY KEY,
  value TEXT
);

CREATE TABLE IF NOT EXISTS digests (
  id           INTEGER PRIMARY KEY,
  sent_at      TEXT NOT NULL,
  window_start TEXT NOT NULL,
  window_end   TEXT NOT NULL,
  email_count  INTEGER NOT NULL
);`

// EmailRow is the durable representation of one source message.
type EmailRow struct {
	ID          int64
	UIDValidity uint32
	IMAPUID     uint32
	MessageID   string
	ReceivedAt  time.Time
	FromName    string
	FromAddr    string
	Subject     string
	RawPath     string
	Summary     string
	ActionReq   string
	Deadline    string
	Tone        string
	InScope     bool
	LLMStatus   string
	LLMError    string
	ProcessedAt time.Time
	DigestedAt  *time.Time
}

// Store owns the SQLite database used by digestd.
type Store struct {
	db *sql.DB
}

// Open opens path and initializes the schema.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// LastUID returns the saved UIDVALIDITY and last processed UID for folder.
func (s *Store) LastUID(folder string) (uint32, uint32, error) {
	validity, err := s.stateUint("uid_validity:" + folder)
	if err != nil {
		return 0, 0, err
	}
	uid, err := s.stateUint("last_uid:" + folder)
	if err != nil {
		return 0, 0, err
	}
	return validity, uid, nil
}

func (s *Store) stateUint(key string) (uint32, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM state WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read state %q: %w", key, err)
	}
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse state %q: %w", key, err)
	}
	return uint32(n), nil
}

// SetLastUID atomically updates the folder synchronization cursor.
func (s *Store) SetLastUID(folder string, uidValidity, lastUID uint32) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin UID state update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setState(tx, "uid_validity:"+folder, strconv.FormatUint(uint64(uidValidity), 10)); err != nil {
		return err
	}
	if err := setState(tx, "last_uid:"+folder, strconv.FormatUint(uint64(lastUID), 10)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit UID state update: %w", err)
	}
	return nil
}

// InsertEmail inserts e unless either deduplication key already exists.
func (s *Store) InsertEmail(e EmailRow) (bool, error) {
	result, err := s.db.Exec(`
INSERT INTO emails (
  uid_validity, imap_uid, message_id, received_at, from_name, from_addr,
  subject, raw_path, summary, action_req, deadline, tone, in_scope,
  llm_status, llm_error, processed_at, digested_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`,
		e.UIDValidity,
		e.IMAPUID,
		nullable(e.MessageID),
		formatTime(e.ReceivedAt),
		nullable(e.FromName),
		nullable(e.FromAddr),
		nullable(e.Subject),
		e.RawPath,
		nullable(e.Summary),
		nullable(e.ActionReq),
		nullable(e.Deadline),
		nullable(e.Tone),
		boolInt(e.InScope),
		defaultString(e.LLMStatus, "ok"),
		nullable(e.LLMError),
		formatTime(e.ProcessedAt),
		nullableTime(e.DigestedAt),
	)
	if err != nil {
		return false, fmt.Errorf("insert email: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("email rows affected: %w", err)
	}
	return n == 1, nil
}

// EmailExists reports whether either deduplication key is already present.
func (s *Store) EmailExists(uidValidity, imapUID uint32, messageID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`
SELECT EXISTS(
  SELECT 1 FROM emails
  WHERE (uid_validity = ? AND imap_uid = ?)
     OR (? <> '' AND message_id = ?)
)`, uidValidity, imapUID, messageID, messageID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check existing email: %w", err)
	}
	return exists != 0, nil
}

// PendingForDigest returns undigested rows in the half-open [start, end) window.
func (s *Store) PendingForDigest(windowStart, windowEnd time.Time) ([]EmailRow, error) {
	rows, err := s.db.Query(`
SELECT id, uid_validity, imap_uid, COALESCE(message_id, ''), received_at,
       COALESCE(from_name, ''), COALESCE(from_addr, ''), COALESCE(subject, ''),
       raw_path, COALESCE(summary, ''), COALESCE(action_req, ''),
       COALESCE(deadline, ''), COALESCE(tone, ''), in_scope,
       COALESCE(llm_status, 'ok'), COALESCE(llm_error, ''), processed_at,
       digested_at
FROM emails
WHERE received_at >= ? AND received_at < ? AND digested_at IS NULL
ORDER BY received_at, id`,
		formatTime(windowStart),
		formatTime(windowEnd),
	)
	if err != nil {
		return nil, fmt.Errorf("select pending digest rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []EmailRow
	for rows.Next() {
		row, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending digest rows: %w", err)
	}
	return result, nil
}

// MarkDigested marks ids as included in a successfully sent digest.
func (s *Store) MarkDigested(ids []int64, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin mark digested: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := markDigested(tx, ids, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark digested: %w", err)
	}
	return nil
}

// RecordDigest records a successful digest and advances the digest window.
func (s *Store) RecordDigest(sentAt, windowStart, windowEnd time.Time, count int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin record digest: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := recordDigest(tx, sentAt, windowStart, windowEnd, count); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record digest: %w", err)
	}
	return nil
}

// CompleteDigest atomically marks rows, records the digest, and advances state.
func (s *Store) CompleteDigest(ids []int64, sentAt, windowStart, windowEnd time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin complete digest: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := markDigested(tx, ids, sentAt); err != nil {
		return err
	}
	if err := recordDigest(tx, sentAt, windowStart, windowEnd, len(ids)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit complete digest: %w", err)
	}
	return nil
}

// LastDigestAt returns the end of the last successfully recorded digest window.
func (s *Store) LastDigestAt() (time.Time, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM state WHERE key = 'last_digest_at'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read last digest time: %w", err)
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last digest time: %w", err)
	}
	return t, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanEmail(s scanner) (EmailRow, error) {
	var row EmailRow
	var receivedAt, processedAt string
	var digestedAt sql.NullString
	var inScope int
	err := s.Scan(
		&row.ID, &row.UIDValidity, &row.IMAPUID, &row.MessageID, &receivedAt,
		&row.FromName, &row.FromAddr, &row.Subject, &row.RawPath, &row.Summary,
		&row.ActionReq, &row.Deadline, &row.Tone, &inScope, &row.LLMStatus,
		&row.LLMError, &processedAt, &digestedAt,
	)
	if err != nil {
		return EmailRow{}, fmt.Errorf("scan email: %w", err)
	}
	row.InScope = inScope != 0
	row.ReceivedAt, err = time.Parse(time.RFC3339, receivedAt)
	if err != nil {
		return EmailRow{}, fmt.Errorf("parse received_at: %w", err)
	}
	row.ProcessedAt, err = time.Parse(time.RFC3339, processedAt)
	if err != nil {
		return EmailRow{}, fmt.Errorf("parse processed_at: %w", err)
	}
	if digestedAt.Valid {
		t, parseErr := time.Parse(time.RFC3339, digestedAt.String)
		if parseErr != nil {
			return EmailRow{}, fmt.Errorf("parse digested_at: %w", parseErr)
		}
		row.DigestedAt = &t
	}
	return row, nil
}

func setState(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`
INSERT INTO state(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set state %q: %w", key, err)
	}
	return nil
}

func markDigested(tx *sql.Tx, ids []int64, at time.Time) error {
	for _, id := range ids {
		if _, err := tx.Exec(
			"UPDATE emails SET digested_at = COALESCE(digested_at, ?) WHERE id = ?",
			formatTime(at), id,
		); err != nil {
			return fmt.Errorf("mark email %d digested: %w", id, err)
		}
	}
	return nil
}

func recordDigest(tx *sql.Tx, sentAt, windowStart, windowEnd time.Time, count int) error {
	if _, err := tx.Exec(`
INSERT INTO digests(sent_at, window_start, window_end, email_count)
VALUES (?, ?, ?, ?)`,
		formatTime(sentAt), formatTime(windowStart), formatTime(windowEnd), count,
	); err != nil {
		return fmt.Errorf("record digest: %w", err)
	}
	if err := setState(tx, "last_digest_at", formatTime(windowEnd)); err != nil {
		return err
	}
	return nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
