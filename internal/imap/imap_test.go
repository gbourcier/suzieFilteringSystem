package imap

import (
	"context"
	"errors"
	"testing"
)

type fakeSession struct {
	validity uint32
	uidNext  uint32
	msgs     []RawMessage
	loginErr error
	since    uint32
	readOnly bool
	seenUIDs []uint32
}

func (f *fakeSession) Login(string, string) error {
	return f.loginErr
}

func (f *fakeSession) Select(_ string, readOnly bool) (mailboxState, error) {
	f.readOnly = readOnly
	return mailboxState{UIDValidity: f.validity, UIDNext: f.uidNext}, nil
}

func (f *fakeSession) FetchSince(since uint32) ([]RawMessage, error) {
	f.since = since
	return f.msgs, nil
}

func (f *fakeSession) MarkSeen(uid uint32) error {
	f.seenUIDs = append(f.seenUIDs, uid)
	return nil
}

func (f *fakeSession) Logout() error {
	return nil
}

func TestLatestUID(t *testing.T) {
	if got := latestUID(635); got != 634 {
		t.Fatalf("latestUID = %d, want 634", got)
	}
	if got := latestUID(0); got != 0 {
		t.Fatalf("latestUID(0) = %d, want 0", got)
	}
}

func TestFetchBaselinesWithoutFetchingExistingMail(t *testing.T) {
	s := &fakeSession{validity: 8, uidNext: 635}
	f := &Fetcher{
		cfg: Config{User: "user", Pass: "pass", Folder: "INBOX"},
		dial: func(context.Context) (session, error) {
			return s, nil
		},
	}

	result, err := f.Fetch(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !result.Baseline || result.UIDValidity != 8 || result.LastUID != 634 {
		t.Fatalf("baseline result = %+v", result)
	}
	if s.since != 0 || !s.readOnly {
		t.Fatalf("baseline fetched mail or selected writable: %+v", s)
	}
}

func TestFetchBaselinesAfterUIDValidityChange(t *testing.T) {
	s := &fakeSession{validity: 8, uidNext: 40}
	f := &Fetcher{
		cfg: Config{User: "user", Pass: "pass", Folder: "INBOX"},
		dial: func(context.Context) (session, error) {
			return s, nil
		},
	}

	result, err := f.Fetch(context.Background(), 7, 99)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !result.Baseline || result.LastUID != 39 || s.since != 0 {
		t.Fatalf("validity-change result = %+v, since=%d", result, s.since)
	}
}

func TestFetchReturnsOnlyNewMailAndSorts(t *testing.T) {
	s := &fakeSession{
		validity: 8,
		uidNext:  104,
		msgs: []RawMessage{
			{UID: 103, Raw: []byte("three")},
			{UID: 101, Raw: []byte("one")},
		},
	}
	f := &Fetcher{
		cfg: Config{User: "user", Pass: "pass", Folder: "INBOX"},
		dial: func(context.Context) (session, error) {
			return s, nil
		},
	}

	result, err := f.Fetch(context.Background(), 8, 100)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Baseline || result.UIDValidity != 8 || s.since != 101 {
		t.Fatalf("result/since = %+v/%d", result, s.since)
	}
	if len(result.Messages) != 2 ||
		result.Messages[0].UID != 101 ||
		result.Messages[1].UID != 103 {
		t.Fatalf("messages = %+v, want sorted UIDs 101,103", result.Messages)
	}
}

func TestMarkSeenUsesWritableMailboxAndChecksValidity(t *testing.T) {
	s := &fakeSession{validity: 8, uidNext: 102}
	f := &Fetcher{
		cfg: Config{User: "user", Pass: "pass", Folder: "INBOX"},
		dial: func(context.Context) (session, error) {
			return s, nil
		},
	}

	if err := f.MarkSeen(context.Background(), 8, 101); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if s.readOnly || len(s.seenUIDs) != 1 || s.seenUIDs[0] != 101 {
		t.Fatalf("MarkSeen session = %+v", s)
	}

	if err := f.MarkSeen(context.Background(), 7, 102); err == nil {
		t.Fatal("MarkSeen returned nil on UIDVALIDITY mismatch")
	}
	if len(s.seenUIDs) != 1 {
		t.Fatalf("mismatched validity marked mail seen: %+v", s.seenUIDs)
	}
}

func TestFetchPropagatesLoginFailure(t *testing.T) {
	s := &fakeSession{loginErr: errors.New("denied")}
	f := &Fetcher{
		dial: func(context.Context) (session, error) {
			return s, nil
		},
	}
	if _, err := f.Fetch(context.Background(), 0, 0); err == nil {
		t.Fatal("Fetch returned nil error on login failure")
	}
}
