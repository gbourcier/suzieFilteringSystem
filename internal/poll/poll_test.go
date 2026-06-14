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
	rows       []store.EmailRow
	cursorUIDs []uint32
	insertErr  error
	exists     bool
}

func (f *fakeStore) LastUID(string) (uint32, uint32, error) {
	return 7, 0, nil
}

func (f *fakeStore) SetLastUID(_ string, _ uint32, uid uint32) error {
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
	messages []mailimap.RawMessage
}

func (f fakeFetcher) Fetch(context.Context, uint32, uint32) (uint32, []mailimap.RawMessage, error) {
	return 7, f.messages, nil
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
	j := New(s, fakeFetcher{messages: []mailimap.RawMessage{{UID: 1, Raw: []byte("raw")}}}, l, Config{
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
	if result.Duplicates != 1 || l.calls != 0 || len(s.cursorUIDs) != 1 {
		t.Fatalf("result/calls/cursor = %+v/%d/%+v", result, l.calls, s.cursorUIDs)
	}
}
