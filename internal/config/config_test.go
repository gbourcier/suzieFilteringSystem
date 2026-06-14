package config

import (
	"testing"
	"time"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"IMAP_HOST":    "imap.example.test",
		"IMAP_USER":    "user",
		"IMAP_PASS":    "pass",
		"SMTP_HOST":    "smtp.example.test",
		"SMTP_USER":    "user",
		"SMTP_PASS":    "pass",
		"SMTP_FROM":    "digest@example.test",
		"DIGEST_TO":    "owner@example.test",
		"OLLAMA_URL":   "http://ollama:11434",
		"OLLAMA_MODEL": "qwen2.5:14b",
		"TZ":           "America/Toronto",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestLoadLLMTimeout(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("LLM_TIMEOUT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLMTimeout != 10*time.Minute {
		t.Fatalf("default LLMTimeout = %v, want 10m", cfg.LLMTimeout)
	}

	t.Setenv("LLM_TIMEOUT", "17m")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load custom timeout: %v", err)
	}
	if cfg.LLMTimeout != 17*time.Minute {
		t.Fatalf("custom LLMTimeout = %v, want 17m", cfg.LLMTimeout)
	}
}

func TestLoadRejectsInvalidLLMTimeout(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("LLM_TIMEOUT", "zero")

	if _, err := Load(); err == nil {
		t.Fatal("Load returned nil error for invalid LLM_TIMEOUT")
	}
}
