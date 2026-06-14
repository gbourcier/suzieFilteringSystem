package poll

import (
	"context"
	"errors"
	"testing"
	"time"

	mailimap "github.com/gbourcier/suzie/internal/imap"
	"github.com/gbourcier/suzie/internal/llm"
	"github.com/gbourcier/suzie/internal/model"
	"github.com/gbourcier/suzie/internal/store"
)

type fakeStore struct {
	validity   uint32
	lastUID    uint32
	rows       []store.EmailRow
	cursorUIDs []uint32
	insertErr  error
	exists     bool
}

func (f *fakeStore) LastUID(string) (uint32, uint32, error) {
	return f.validity, f.lastUID, nil
}

func (f *fakeStore) SetLastUID(_ string, validity, uid uint32) error {
	f.validity = validity
	f.lastUID = uid
	f.cursorUIDs = append(f.cursorUIDs, uid)
	return nil
}

func (f *fakeStore) EmailExists(uint32, uint32, string) (bool, error) {
	return f.exists, nil
}

func (f *fakeStore) InsertEmail(row store.EmailRow) (bool, error) {
	if f.insertErr != nil {
		return false, f.insertErr
	}
	f.rows = append(f.rows, row)
	return true, nil
}

type fakeFetcher struct {
	result      mailimap.FetchResult
	seenUIDs    []uint32
	markSeenErr error
}

func (f *fakeFetcher) Fetch(context.Context, uint32, uint32) (mailimap.FetchResult, error) {
	return f.result, nil
}

func (f *fakeFetcher) MarkSeen(_ context.Context, _ uint32, uid uint32) error {
	if f.markSeenErr != nil {
		return f.markSeenErr
	}
	f.seenUIDs = append(f.seenUIDs, uid)
	return nil
}

type fakeLLM struct {
	calls int
}

func (f *fakeLLM) Summarize(context.Context, llm.Input) (model.Summary, llm.RawDebug) {
	f.calls++
	return model.Summary{
		Text:   "Résumé.",
		Action: "fyi",
		Tone:   "neutral",
		Status: "ok",
	}, llm.RawDebug{}
}

func newTestJob(s *fakeStore, l *fakeLLM) *Job {
	if s.validity == 0 {
		s.validity = 7
	}
	f := &fakeFetcher{result: mailimap.FetchResult{
		UIDValidity: 7,
		Messages:    []mailimap.RawMessage{{UID: 1, Raw: []byte("raw")}},
	}}
	j := New(s, f, l, Config{
		Folder:          "INBOX",
		ArchiveDir:      "/unused",
		BodyCharLimit:   4000,
		SummaryLanguage: "fr",
		LLMTimeout:      time.Minute,
	})
	j.now = func() time.Time {
		return time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	}
	j.archive = func(string, time.Time, string, uint32, []byte) (string, error) {
		return "/archive/message.eml", nil
	}
	return j
}

func TestRunInitializesBaselineWithoutProcessingBacklog(t *testing.T) {
	s := &fakeStore{}
	l := &fakeLLM{}
	f := &fakeFetcher{result: mailimap.FetchResult{
		UIDValidity: 42,
		LastUID:     100,
		Baseline:    true,
	}}
	j := New(s, f, l, Config{Folder: "INBOX/Board"})

	result, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Initialized || s.validity != 42 || s.lastUID != 100 {
		t.Fatalf("result/store = %+v / %+v", result, s)
	}
	if len(s.rows) != 0 || l.calls != 0 || len(f.seenUIDs) != 0 {
		t.Fatalf("baseline processed existing mail: rows=%d llm=%d seen=%v",
			len(s.rows), l.calls, f.seenUIDs)
	}
}

func TestRunFailOpenOnParseError(t *testing.T) {
	s := &fakeStore{}
	l := &fakeLLM{}
	j := newTestJob(s, l)
	j.parse = func([]byte, int) (model.Parsed, error) {
		return model.Parsed{}, errors.New("bad MIME")
	}

	result, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Errors != 1 || len(s.rows) != 1 {
		t.Fatalf("result/rows = %+v/%d, want one persisted error", result, len(s.rows))
	}
	if s.rows[0].RawPath == "" || s.rows[0].LLMStatus != "error" {
		t.Fatalf("persisted row = %+v", s.rows[0])
	}
	if l.calls != 0 {
		t.Fatalf("LLM calls = %d, want 0", l.calls)
	}
}

func TestRunSkipsLLMForOutOfScope(t *testing.T) {
	s := &fakeStore{}
	l := &fakeLLM{}
	j := newTestJob(s, l)
	j.allowlist = map[string]struct{}{"board@example.test": {}}
	j.parse = func([]byte, int) (model.Parsed, error) {
		return model.Parsed{
			MessageID: "other@example.test",
			From:      model.Address{Addr: "other@example.test"},
		}, nil
	}

	result, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.OutOfScope != 1 || s.rows[0].InScope {
		t.Fatalf("result/row = %+v/%+v", result, s.rows[0])
	}
	if l.calls != 0 {
		t.Fatalf("LLM calls = %d, want 0", l.calls)
	}
}

func TestRunAdvancesCursorOnlyAfterInsert(t *testing.T) {
	s := &fakeStore{insertErr: errors.New("disk full")}
	l := &fakeLLM{}
	j := newTestJob(s, l)
	j.parse = func([]byte, int) (model.Parsed, error) {
		return model.Parsed{
			MessageID: "message@example.test",
			From:      model.Address{Addr: "board@example.test"},
		}, nil
	}

	if _, err := j.Run(context.Background()); err == nil {
		t.Fatal("Run returned nil error on insert failure")
	}
	if len(s.cursorUIDs) != 0 {
		t.Fatalf("cursor advanced before persistence: %+v", s.cursorUIDs)
	}
}

func TestRunAdvancesCursorOnlyAfterMarkSeen(t *testing.T) {
	s := &fakeStore{}
	l := &fakeLLM{}
	j := newTestJob(s, l)
	f := j.fetcher.(*fakeFetcher)
	f.markSeenErr = errors.New("cannot set flag")
	j.parse = func([]byte, int) (model.Parsed, error) {
		return model.Parsed{
			MessageID: "message@example.test",
			From:      model.Address{Addr: "board@example.test"},
		}, nil
	}

	if _, err := j.Run(context.Background()); err == nil {
		t.Fatal("Run returned nil error on MarkSeen failure")
	}
	if len(s.rows) != 1 {
		t.Fatalf("persisted rows = %d, want 1", len(s.rows))
	}
	if len(s.cursorUIDs) != 0 {
		t.Fatalf("cursor advanced before MarkSeen: %+v", s.cursorUIDs)
	}
}

func TestRunSkipsExistingMessageBeforeLLM(t *testing.T) {
	s := &fakeStore{exists: true}
	l := &fakeLLM{}
	j := newTestJob(s, l)
	j.parse = func([]byte, int) (model.Parsed, error) {
		return model.Parsed{MessageID: "known@example.test"}, nil
	}

	result, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	f := j.fetcher.(*fakeFetcher)
	if result.Duplicates != 1 ||
		l.calls != 0 ||
		len(f.seenUIDs) != 1 ||
		len(s.cursorUIDs) != 1 {
		t.Fatalf("result/calls/seen/cursor = %+v/%d/%+v/%+v",
			result, l.calls, f.seenUIDs, s.cursorUIDs)
	}
}
