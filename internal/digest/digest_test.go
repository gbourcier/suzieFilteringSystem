package digest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gbourcier/suzie/internal/store"
)

func TestRenderGolden(t *testing.T) {
	loc := time.FixedZone("EST", -5*60*60)
	start := time.Date(2026, time.June, 1, 8, 0, 0, 0, loc)
	view := View{
		WindowStart: start,
		WindowEnd:   start.Add(7 * 24 * time.Hour),
		Rows: []store.EmailRow{
			{
				ReceivedAt: start.Add(time.Hour),
				FromName:   "Board",
				FromAddr:   "board@example.test",
				Subject:    "Meeting <details>",
				Summary:    "The board requests confirmation.",
				ActionReq:  "reply",
				Deadline:   "2026-06-05",
				Tone:       "heated",
				InScope:    true,
				LLMStatus:  "ok",
				RawPath:    "/data/archive/2026/06/one.eml",
			},
			{
				ReceivedAt: start.Add(2 * time.Hour),
				FromAddr:   "other@example.test",
				Subject:    "Automated notice",
				Summary:    "could not summarize - read original",
				InScope:    false,
				LLMStatus:  "error",
				RawPath:    "/data/archive/2026/06/two.eml",
			},
		},
	}

	html, err := RenderHTML(view)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text, err := RenderText(view)
	if err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	assertGolden(t, "digest.html.golden", html)
	assertGolden(t, "digest.txt.golden", text)
}

func TestRenderEmptyGolden(t *testing.T) {
	start := time.Date(2026, time.June, 1, 8, 0, 0, 0, time.UTC)
	view := View{WindowStart: start, WindowEnd: start.Add(7 * 24 * time.Hour)}

	html, err := RenderHTML(view)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text, err := RenderText(view)
	if err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	assertGolden(t, "empty.html.golden", html)
	assertGolden(t, "empty.txt.golden", text)
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o640); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("%s does not match; run UPDATE_GOLDEN=1 go test ./internal/digest", name)
	}
}
