package imap

import (
	"context"
	"errors"
	"math"
	"testing"
)

type fakeSession struct {
	validity uint32
	msgs     []RawMessage
	loginErr error
	since    uint32
}

func (f *fakeSession) Login(string, string) error {
	return f.loginErr
}

func (f *fakeSession) Select(string) (uint32, error) {
	return f.validity, nil
}

func (f *fakeSession) FetchSince(since uint32) ([]RawMessage, error) {
	f.since = since
	return f.msgs, nil
}

func (f *fakeSession) Logout() error {
	return nil
}

func TestFirstUID(t *testing.T) {
	tests := []struct {
		name                 string
		known, current, last uint32
		want                 uint32
	}{
		{name: "initial sync", current: 7, want: 1},
		{name: "same validity resumes", known: 7, current: 7, last: 12, want: 13},
		{name: "changed validity resyncs", known: 7, current: 8, last: 12, want: 1},
		{name: "maximum UID has no successor", known: 7, current: 7, last: math.MaxUint32, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstUID(tc.known, tc.current, tc.last); got != tc.want {
				t.Fatalf("firstUID = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFetchUsesValidityDecisionAndSorts(t *testing.T) {
	s := &fakeSession{
		validity: 8,
		msgs: []RawMessage{
			{UID: 3, Raw: []byte("three")},
			{UID: 1, Raw: []byte("one")},
		},
	}
	f := &Fetcher{
		cfg: Config{User: "user", Pass: "pass", Folder: "INBOX"},
		dial: func(context.Context) (session, error) {
			return s, nil
		},
	}

	validity, msgs, err := f.Fetch(context.Background(), 7, 99)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if validity != 8 || s.since != 1 {
		t.Fatalf("validity/since = %d/%d, want 8/1", validity, s.since)
	}
	if len(msgs) != 2 || msgs[0].UID != 1 || msgs[1].UID != 3 {
		t.Fatalf("messages = %+v, want sorted UIDs 1,3", msgs)
	}
}

func TestFetchPropagatesLoginFailure(t *testing.T) {
	s := &fakeSession{loginErr: errors.New("denied")}
	f := &Fetcher{
		dial: func(context.Context) (session, error) {
			return s, nil
		},
	}
	if _, _, err := f.Fetch(context.Background(), 0, 0); err == nil {
		t.Fatal("Fetch returned nil error on login failure")
	}
}
