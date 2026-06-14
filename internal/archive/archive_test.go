package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	received := time.Date(2026, time.February, 3, 12, 0, 0, 0, time.UTC)
	first := []byte("first")

	path, err := Write(dir, received, "<message/id@example.test>", 12, first)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("2026", "02", "message_id_example.test.eml")) {
		t.Fatalf("path = %q", path)
	}

	path2, err := Write(dir, received, "<message/id@example.test>", 12, first)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if path2 != path {
		t.Fatalf("second path = %q, want %q", path2, path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(first) {
		t.Fatalf("archive was overwritten: %q", got)
	}
}

func TestWriteFallsBackToUID(t *testing.T) {
	dir := t.TempDir()
	received := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	path, err := Write(dir, received, "", 44, []byte("raw"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Base(path) != "uid-44.eml" {
		t.Fatalf("base path = %q, want uid-44.eml", filepath.Base(path))
	}

	second, err := Write(dir, received, "", 44, []byte("different raw message"))
	if err != nil {
		t.Fatalf("Write colliding UID: %v", err)
	}
	if second == path || filepath.Base(second) == "uid-44.eml" {
		t.Fatalf("colliding UID path = %q, want collision-safe alternate", second)
	}
}
