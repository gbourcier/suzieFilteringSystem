package main

import (
	"context"
	_ "time/tzdata" // embed timezone database for distroless/scratch images

	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gbourcier/suzie/internal/config"
	mailimap "github.com/gbourcier/suzie/internal/imap"
	"github.com/gbourcier/suzie/internal/llm"
	"github.com/gbourcier/suzie/internal/mailer"
	"github.com/gbourcier/suzie/internal/poll"
	"github.com/gbourcier/suzie/internal/scheduler"
	"github.com/gbourcier/suzie/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})))

	slog.Info("digestd starting", "config", cfg.Redacted())

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := db.Close(); err != nil {
			slog.Error("close store", "err", err)
		}
	}()

	fetcher := mailimap.New(mailimap.Config{
		Host:   cfg.IMAPHost,
		Port:   cfg.IMAPPort,
		User:   cfg.IMAPUser,
		Pass:   cfg.IMAPPass,
		Folder: cfg.IMAPFolder,
	})
	llmClient := llm.New(cfg.OllamaURL, cfg.OllamaModel, cfg.LLMTimeout)
	smtpMailer, err := mailer.New(mailer.Config{
		Host: cfg.SMTPHost,
		Port: cfg.SMTPPort,
		User: cfg.SMTPUser,
		Pass: cfg.SMTPPass,
		From: cfg.SMTPFrom,
	})
	if err != nil {
		slog.Error("initialize mailer", "err", err)
		os.Exit(1)
	}
	pollJob := poll.New(db, fetcher, llmClient, poll.Config{
		Folder:          cfg.IMAPFolder,
		ArchiveDir:      cfg.ArchiveDir,
		Allowlist:       cfg.IMAPAllowlist,
		BodyCharLimit:   cfg.BodyCharLimit,
		SummaryLanguage: cfg.SummaryLanguage,
		LLMTimeout:      cfg.LLMTimeout,
	})
	jobScheduler := scheduler.New(scheduler.Config{
		PollSchedule:   cfg.PollSchedule,
		DigestSchedule: cfg.DigestSchedule,
		DigestTo:       cfg.DigestTo,
		Location:       cfg.TZ,
	}, pollJob, db, smtpMailer)
	if err := jobScheduler.Start(); err != nil {
		slog.Error("start scheduler", "err", err)
		os.Exit(1)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	sig := <-signals
	signal.Stop(signals)
	slog.Info("shutdown requested", "signal", sig.String())

	if err := jobScheduler.Stop(context.Background()); err != nil {
		slog.Error("stop scheduler", "err", err)
		os.Exit(1)
	}
	slog.Info("digestd stopped")
}
