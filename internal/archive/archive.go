package archive

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxBaseName = 120

// Write atomically archives raw under dir/YYYY/MM and returns the final path.
func Write(dir string, received time.Time, messageID string, uid uint32, raw []byte) (string, error) {
	if received.IsZero() {
		received = time.Now()
	}
	monthDir := filepath.Join(dir, received.Format("2006"), received.Format("01"))
	if err := os.MkdirAll(monthDir, 0o750); err != nil {
		return "", fmt.Errorf("create archive directory: %w", err)
	}

	name := safeName(messageID)
	if name == "" {
		name = fmt.Sprintf("uid-%d", uid)
	}
	target, exists, err := archiveTarget(filepath.Join(monthDir, name+".eml"), raw)
	if err != nil {
		return "", err
	}
	if exists {
		return target, nil
	}

	tmp, err := os.CreateTemp(monthDir, ".digestd-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create archive temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("set archive permissions: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write archive temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("sync archive temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close archive temp file: %w", err)
	}

	// Link publishes the complete temp file without replacing an existing archive.
	if err := os.Link(tmpPath, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(target)
			if readErr == nil && bytes.Equal(existing, raw) {
				return target, nil
			}
		}
		return "", fmt.Errorf("publish archive file: %w", err)
	}
	return target, nil
}

func archiveTarget(base string, raw []byte) (string, bool, error) {
	existing, err := os.ReadFile(base)
	if err == nil {
		if bytes.Equal(existing, raw) {
			return base, true, nil
		}
		sum := sha256.Sum256(raw)
		ext := filepath.Ext(base)
		alternate := strings.TrimSuffix(base, ext) + fmt.Sprintf("-%x%s", sum[:6], ext)
		alternateRaw, alternateErr := os.ReadFile(alternate)
		switch {
		case alternateErr == nil && bytes.Equal(alternateRaw, raw):
			return alternate, true, nil
		case alternateErr == nil:
			return "", false, fmt.Errorf("archive hash collision at %q", alternate)
		case errors.Is(alternateErr, os.ErrNotExist):
			return alternate, false, nil
		default:
			return "", false, fmt.Errorf("inspect alternate archive target: %w", alternateErr)
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return base, false, nil
	}
	return "", false, fmt.Errorf("inspect archive target: %w", err)
}

func safeName(messageID string) string {
	messageID = strings.Trim(messageID, "<> \t")
	var b strings.Builder
	for _, r := range messageID {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= maxBaseName {
			break
		}
	}
	name := strings.Trim(b.String(), ".")
	if name == "" || name == ".." {
		return ""
	}
	return name
}
