package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	IMAPHost     string
	IMAPPort     int
	IMAPUser     string
	IMAPPass     string
	IMAPFolder   string
	IMAPAllowlist []string // empty = allowlist off

	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	SMTPFrom string
	DigestTo string

	OllamaURL   string
	OllamaModel string

	SummaryLanguage string
	PollSchedule    string
	DigestSchedule  string
	BodyCharLimit   int

	DBPath     string
	ArchiveDir string

	TZ       *time.Location
	LogLevel slog.Level
}

// Load reads configuration from environment variables.
// It returns an error listing all missing required variables.
func Load() (*Config, error) {
	c := &Config{}
	var missing []string

	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	optStr := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	optInt := func(key string, def int) int {
		v := os.Getenv(key)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return n
	}

	c.IMAPHost = required("IMAP_HOST")
	c.IMAPPort = optInt("IMAP_PORT", 993)
	c.IMAPUser = required("IMAP_USER")
	c.IMAPPass = required("IMAP_PASS")
	c.IMAPFolder = optStr("IMAP_FOLDER", "INBOX")

	if al := os.Getenv("IMAP_ALLOWLIST"); al != "" {
		for _, addr := range strings.Split(al, ",") {
			addr = strings.ToLower(strings.TrimSpace(addr))
			if addr != "" {
				c.IMAPAllowlist = append(c.IMAPAllowlist, addr)
			}
		}
	}

	c.SMTPHost = required("SMTP_HOST")
	c.SMTPPort = optInt("SMTP_PORT", 587)
	c.SMTPUser = required("SMTP_USER")
	c.SMTPPass = required("SMTP_PASS")
	c.SMTPFrom = required("SMTP_FROM")
	c.DigestTo = required("DIGEST_TO")

	c.OllamaURL = required("OLLAMA_URL")
	c.OllamaModel = required("OLLAMA_MODEL")

	c.SummaryLanguage = optStr("SUMMARY_LANGUAGE", "fr")
	c.PollSchedule = optStr("POLL_SCHEDULE", "0 7 * * *")
	c.DigestSchedule = optStr("DIGEST_SCHEDULE", "0 8 * * 1")
	c.BodyCharLimit = optInt("BODY_CHAR_LIMIT", 4000)
	c.DBPath = optStr("DB_PATH", "/data/digestd.db")
	c.ArchiveDir = optStr("ARCHIVE_DIR", "/data/archive")

	tzName := optStr("TZ", "America/Toronto")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("invalid TZ %q: %w", tzName, err)
	}
	c.TZ = loc

	level := slog.LevelInfo
	if ls := os.Getenv("LOG_LEVEL"); ls != "" {
		if err := level.UnmarshalText([]byte(ls)); err != nil {
			return nil, fmt.Errorf("invalid LOG_LEVEL %q: %w", ls, err)
		}
	}
	c.LogLevel = level

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// Redacted returns a copy of the config safe for logging (passwords masked).
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"imap_host":        c.IMAPHost,
		"imap_port":        c.IMAPPort,
		"imap_user":        c.IMAPUser,
		"imap_pass":        "***",
		"imap_folder":      c.IMAPFolder,
		"imap_allowlist":   c.IMAPAllowlist,
		"smtp_host":        c.SMTPHost,
		"smtp_port":        c.SMTPPort,
		"smtp_user":        c.SMTPUser,
		"smtp_pass":        "***",
		"smtp_from":        c.SMTPFrom,
		"digest_to":        c.DigestTo,
		"ollama_url":       c.OllamaURL,
		"ollama_model":     c.OllamaModel,
		"summary_language": c.SummaryLanguage,
		"poll_schedule":    c.PollSchedule,
		"digest_schedule":  c.DigestSchedule,
		"body_char_limit":  c.BodyCharLimit,
		"db_path":          c.DBPath,
		"archive_dir":      c.ArchiveDir,
		"tz":               c.TZ.String(),
		"log_level":        c.LogLevel.String(),
	}
}
