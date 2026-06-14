package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "digestd.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func testRow(uid uint32, received time.Time) EmailRow {
	return EmailRow{
		UIDValidity: 7,
		IMAPUID:     uid,
		MessageID:   "message-" + received.Format("20060102T150405") + "@example.test",
		ReceivedAt:  received,
		FromAddr:    "board@example.test",
		Subject:     "Test",
		RawPath:     "/archive/test.eml",
		Summary:     "Summary",
		ActionReq:   "fyi",
		Tone:        "neutral",
		InScope:     true,
		LLMStatus:   "ok",
		ProcessedAt: received.Add(time.Minute),
	}
}

func TestInsertEmailDeduplicates(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	row := testRow(10, now)

	inserted, err := s.InsertEmail(row)
	if err != nil || !inserted {
		t.Fatalf("first InsertEmail = %v, %v; want true, nil", inserted, err)
	}
	inserted, err = s.InsertEmail(row)
	if err != nil || inserted {
		t.Fatalf("duplicate InsertEmail = %v, %v; want false, nil", inserted, err)
	}

	row.IMAPUID = 11
	inserted, err = s.InsertEmail(row)
	if err != nil || inserted {
		t.Fatalf("Message-ID duplicate InsertEmail = %v, %v; want false, nil", inserted, err)
	}

	exists, err := s.EmailExists(999, 999, row.MessageID)
	if err != nil || !exists {
		t.Fatalf("EmailExists by Message-ID = %v, %v; want true, nil", exists, err)
	}
}

func TestInsertEmailAllowsMissingMessageIDs(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	first := testRow(10, now)
	first.MessageID = ""
	second := testRow(11, now.Add(time.Second))
	second.MessageID = ""

	for _, row := range []EmailRow{first, second} {
		inserted, err := s.InsertEmail(row)
		if err != nil || !inserted {
			t.Fatalf("InsertEmail = %v, %v; want true, nil", inserted, err)
		}
	}
}

func TestPendingForDigestWindowAndMarking(t *testing.T) {
	s := openTestStore(t)
	start := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)

	rows := []EmailRow{
		testRow(1, start.Add(-time.Second)),
		testRow(2, start),
		testRow(3, end.Add(-time.Second)),
		testRow(4, end),
	}
	for _, row := range rows {
		if _, err := s.InsertEmail(row); err != nil {
			t.Fatalf("InsertEmail: %v", err)
		}
	}

	pending, err := s.PendingForDigest(start, end)
	if err != nil {
		t.Fatalf("PendingForDigest: %v", err)
	}
	if len(pending) != 2 || pending[0].IMAPUID != 2 || pending[1].IMAPUID != 3 {
		t.Fatalf("pending UIDs = %+v, want [2 3]", pending)
	}

	ids := []int64{pending[0].ID, pending[1].ID}
	if err := s.MarkDigested(ids, end); err != nil {
		t.Fatalf("MarkDigested: %v", err)
	}
	if err := s.MarkDigested(ids, end.Add(time.Hour)); err != nil {
		t.Fatalf("second MarkDigested: %v", err)
	}
	pending, err = s.PendingForDigest(start, end)
	if err != nil {
		t.Fatalf("PendingForDigest after mark: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after mark = %d, want 0", len(pending))
	}
}

func TestUIDStateAndCompleteDigest(t *testing.T) {
	s := openTestStore(t)

	validity, uid, err := s.LastUID("INBOX")
	if err != nil || validity != 0 || uid != 0 {
		t.Fatalf("empty LastUID = %d, %d, %v", validity, uid, err)
	}
	if err := s.SetLastUID("INBOX", 42, 99); err != nil {
		t.Fatalf("SetLastUID: %v", err)
	}
	validity, uid, err = s.LastUID("INBOX")
	if err != nil || validity != 42 || uid != 99 {
		t.Fatalf("LastUID = %d, %d, %v; want 42, 99, nil", validity, uid, err)
	}

	start := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	row := testRow(100, start.Add(time.Hour))
	if _, err := s.InsertEmail(row); err != nil {
		t.Fatalf("InsertEmail: %v", err)
	}
	pending, err := s.PendingForDigest(start, end)
	if err != nil {
		t.Fatalf("PendingForDigest: %v", err)
	}
	if err := s.CompleteDigest([]int64{pending[0].ID}, end, start, end); err != nil {
		t.Fatalf("CompleteDigest: %v", err)
	}
	last, err := s.LastDigestAt()
	if err != nil || !last.Equal(end) {
		t.Fatalf("LastDigestAt = %v, %v; want %v", last, err, end)
	}
}
