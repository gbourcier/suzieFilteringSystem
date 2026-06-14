package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ollamaBody builds a fake Ollama /api/chat response with the given content string.
func ollamaBody(content string) string {
	b, _ := json.Marshal(content)
	return fmt.Sprintf(`{"model":"test","message":{"role":"assistant","content":%s}}`, string(b))
}

const validContent = `{"summary":"Le syndicat informe d'une cotisation spéciale.","action_required":"action","deadline":"2025-06-30","tone":"neutral"}`
const noDeadlineContent = `{"summary":"Le syndicat informe des travaux prévus.","action_required":"none","deadline":null,"tone":"neutral"}`

func TestSummarize_ValidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w,ollamaBody(validContent))
	}))
	defer srv.Close()

	c := New(srv.URL, "test", 5*time.Second)
	s, dbg := c.Summarize(context.Background(), Input{
		From: "board@condo.tld", Subject: "Test", Body: "Corps du message.", Language: "fr",
	})

	if s.Status != "ok" {
		t.Fatalf("Status = %q, want ok; Err=%q", s.Status, s.Err)
	}
	if s.Text == "" || s.Text == fallbackText {
		t.Errorf("unexpected Text: %q", s.Text)
	}
	if s.Action != "action" {
		t.Errorf("Action = %q, want action", s.Action)
	}
	if s.Deadline != "2025-06-30" {
		t.Errorf("Deadline = %q, want 2025-06-30", s.Deadline)
	}
	if s.Tone != "neutral" {
		t.Errorf("Tone = %q, want neutral", s.Tone)
	}
	if dbg.Retried {
		t.Error("Retried should be false on first-try success")
	}
	if dbg.LatencyMS < 0 {
		t.Error("LatencyMS should be non-negative")
	}
}

func TestSummarize_NullDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w,ollamaBody(noDeadlineContent))
	}))
	defer srv.Close()

	c := New(srv.URL, "test", 5*time.Second)
	s, _ := c.Summarize(context.Background(), Input{Body: "x", Language: "fr"})

	if s.Status != "ok" {
		t.Fatalf("Status = %q", s.Status)
	}
	if s.Deadline != "" {
		t.Errorf("Deadline = %q, want empty", s.Deadline)
	}
}

func TestSummarize_InvalidJsonThenValid_Retries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First call: bad JSON in content
			_, _ = fmt.Fprint(w,ollamaBody("not valid json"))
		} else {
			_, _ = fmt.Fprint(w,ollamaBody(validContent))
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "test", 5*time.Second)
	s, dbg := c.Summarize(context.Background(), Input{Body: "x", Language: "fr"})

	if s.Status != "ok" {
		t.Fatalf("Status = %q after retry, want ok; Err=%q", s.Status, s.Err)
	}
	if !dbg.Retried {
		t.Error("Retried should be true")
	}
	if calls.Load() != 2 {
		t.Errorf("expected exactly 2 HTTP calls, got %d", calls.Load())
	}
}

func TestSummarize_InvalidJsonTwice_Fallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w,ollamaBody("this is not json at all"))
	}))
	defer srv.Close()

	c := New(srv.URL, "test", 5*time.Second)
	s, dbg := c.Summarize(context.Background(), Input{Body: "x", Language: "fr"})

	if s.Status != "error" {
		t.Fatalf("Status = %q, want error", s.Status)
	}
	if s.Text != fallbackText {
		t.Errorf("Text = %q, want %q", s.Text, fallbackText)
	}
	if !dbg.Retried {
		t.Error("Retried should be true after two failures")
	}
}

func TestSummarize_HttpError_Fallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "test", 5*time.Second)
	s, _ := c.Summarize(context.Background(), Input{Body: "x", Language: "fr"})

	if s.Status != "error" {
		t.Fatalf("Status = %q, want error on HTTP 500", s.Status)
	}
	if s.Text != fallbackText {
		t.Errorf("Text = %q, want fallback", s.Text)
	}
}

func TestSummarize_Timeout_Fallback(t *testing.T) {
	// Server that sleeps longer than the client timeout
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = fmt.Fprint(w,ollamaBody(validContent))
	}))
	defer srv.Close()

	c := New(srv.URL, "test", 10*time.Millisecond) // very short timeout
	s, _ := c.Summarize(context.Background(), Input{Body: "x", Language: "fr"})

	if s.Status != "error" {
		t.Fatalf("Status = %q, want error on timeout", s.Status)
	}
	if s.Text != fallbackText {
		t.Errorf("Text = %q, want fallback", s.Text)
	}
}

func TestSummarize_FailOpenContract(t *testing.T) {
	// Verifies the core invariant: Summarize always returns a persistable Summary.
	// Uses a server that returns garbage to force the fallback path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return completely invalid response body
		_, _ = fmt.Fprint(w,`{"model":"test","message":{"role":"assistant","content":""}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "test", 5*time.Second)
	s, _ := c.Summarize(context.Background(), Input{Body: "x", Language: "fr"})

	// Contract: Status is always "ok" or "error", never empty
	if s.Status != "ok" && s.Status != "error" {
		t.Errorf("Status = %q: must be ok or error", s.Status)
	}
	// Text is always non-empty (either the summary or the fallback)
	if s.Text == "" {
		t.Error("Text must not be empty — row must always be persistable")
	}
}

func TestValidate_SemanticChecks(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantOK  bool
	}{
		{
			name:   "valid full",
			raw:    `{"summary":"Résumé neutre.","action_required":"none","deadline":null,"tone":"neutral"}`,
			wantOK: true,
		},
		{
			name:   "valid with deadline",
			raw:    `{"summary":"Résumé.","action_required":"reply","deadline":"2025-12-31","tone":"heated"}`,
			wantOK: true,
		},
		{
			name:   "empty summary",
			raw:    `{"summary":"","action_required":"none","deadline":null,"tone":"neutral"}`,
			wantOK: false,
		},
		{
			name:   "invalid action",
			raw:    `{"summary":"Résumé.","action_required":"urgent","deadline":null,"tone":"neutral"}`,
			wantOK: false,
		},
		{
			name:   "invalid tone",
			raw:    `{"summary":"Résumé.","action_required":"none","deadline":null,"tone":"angry"}`,
			wantOK: false,
		},
		{
			name:   "invalid deadline format",
			raw:    `{"summary":"Résumé.","action_required":"none","deadline":"31/12/2025","tone":"neutral"}`,
			wantOK: false,
		},
		{
			name:   "empty deadline string treated as null",
			raw:    `{"summary":"Résumé.","action_required":"none","deadline":"","tone":"neutral"}`,
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := validate(tc.raw)
			if tc.wantOK && err != nil {
				t.Errorf("validate error: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("expected error, got summary: %+v", s)
			}
		})
	}
}
