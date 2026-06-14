// Package scheduler coordinates polling and weekly digest delivery.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/gbourcier/suzie/internal/digest"
	"github.com/gbourcier/suzie/internal/poll"
	"github.com/gbourcier/suzie/internal/store"
)

// Config controls schedules and digest delivery.
type Config struct {
	PollSchedule   string
	DigestSchedule string
	DigestTo       string
	Location       *time.Location
}

type poller interface {
	Run(context.Context) (poll.Result, error)
}

type digestStore interface {
	LastDigestAt() (time.Time, error)
	PendingForDigest(time.Time, time.Time) ([]store.EmailRow, error)
	CompleteDigest([]int64, time.Time, time.Time, time.Time) error
}

type mailer interface {
	Send(context.Context, string, string, string, string) error
}

// Scheduler owns cron registration and a nonblocking process-wide run lock.
type Scheduler struct {
	cfg      Config
	poller   poller
	store    digestStore
	mailer   mailer
	cron     *cron.Cron
	lock     chan struct{}
	now      func() time.Time
	stopping atomic.Bool
}

// New constructs a scheduler.
func New(cfg Config, p poller, s digestStore, m mailer) *Scheduler {
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	return &Scheduler{
		cfg:    cfg,
		poller: p,
		store:  s,
		mailer: m,
		cron:   cron.New(cron.WithLocation(cfg.Location)),
		lock:   make(chan struct{}, 1),
		now:    time.Now,
	}
}

// Start registers both schedules, starts cron, and launches one startup poll.
func (s *Scheduler) Start() error {
	if _, err := s.cron.AddFunc(s.cfg.PollSchedule, func() {
		s.RunPoll(context.Background())
	}); err != nil {
		return fmt.Errorf("register poll schedule: %w", err)
	}
	if _, err := s.cron.AddFunc(s.cfg.DigestSchedule, func() {
		s.RunDigest(context.Background())
	}); err != nil {
		return fmt.Errorf("register digest schedule: %w", err)
	}
	s.cron.Start()
	go s.RunPoll(context.Background())
	return nil
}

// Stop prevents new cron runs and waits for an active cron callback.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.stopping.Store(true)
	stopped := s.cron.Stop()
	select {
	case <-stopped.Done():
	case <-ctx.Done():
		return ctx.Err()
	}

	// Acquiring the run lock waits for the startup poll or active job to finish.
	select {
	case s.lock <- struct{}{}:
		<-s.lock
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunPoll runs a poll if no other job currently owns the process lock.
func (s *Scheduler) RunPoll(ctx context.Context) bool {
	return s.withLock("poll", func() {
		result, err := s.poller.Run(ctx)
		if err != nil {
			slog.Error("poll job failed", "err", err)
			return
		}
		slog.Info("poll job complete",
			"initialized", result.Initialized,
			"fetched", result.Fetched,
			"processed", result.Processed,
			"duplicates", result.Duplicates,
			"errors", result.Errors,
			"out_of_scope", result.OutOfScope,
		)
	})
}

// RunDigest sends and completes a digest if no other job owns the process lock.
func (s *Scheduler) RunDigest(ctx context.Context) bool {
	return s.withLock("digest", func() {
		if err := s.runDigest(ctx); err != nil {
			slog.Error("digest job failed", "err", err)
		}
	})
}

func (s *Scheduler) runDigest(ctx context.Context) error {
	windowEnd := s.now().In(s.cfg.Location)
	windowStart, err := s.store.LastDigestAt()
	if err != nil {
		return err
	}
	if windowStart.IsZero() {
		windowStart = windowEnd.AddDate(0, 0, -7)
	} else {
		windowStart = windowStart.In(s.cfg.Location)
	}

	rows, err := s.store.PendingForDigest(windowStart, windowEnd)
	if err != nil {
		return err
	}
	view := digest.View{WindowStart: windowStart, WindowEnd: windowEnd, Rows: rows}
	htmlBody, err := digest.RenderHTML(view)
	if err != nil {
		return err
	}
	textBody, err := digest.RenderText(view)
	if err != nil {
		return err
	}
	subject := "Weekly email digest - " + windowEnd.Format("2006-01-02")
	if err := s.mailer.Send(ctx, s.cfg.DigestTo, subject, htmlBody, textBody); err != nil {
		return err
	}

	ids := make([]int64, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	if err := s.store.CompleteDigest(ids, windowEnd, windowStart, windowEnd); err != nil {
		return err
	}
	slog.Info("digest job complete", "email_count", len(rows))
	return nil
}

func (s *Scheduler) withLock(name string, job func()) bool {
	if s.stopping.Load() {
		return false
	}
	select {
	case s.lock <- struct{}{}:
		if s.stopping.Load() {
			<-s.lock
			return false
		}
		defer func() { <-s.lock }()
		job()
		return true
	default:
		slog.Warn("job skipped because another job is running", "job", name)
		return false
	}
}
