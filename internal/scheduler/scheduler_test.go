package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gbourcier/suzie/internal/poll"
	"github.com/gbourcier/suzie/internal/store"
)

type fakePoller struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (f *fakePoller) Run(context.Context) (poll.Result, error) {
	f.calls.Add(1)
	f.once.Do(func() {
		if f.started != nil {
			close(f.started)
		}
	})
	if f.release != nil {
		<-f.release
	}
	return poll.Result{}, nil
}

type fakeDigestStore struct {
	last          time.Time
	rows          []store.EmailRow
	completeCalls int
}

func (f *fakeDigestStore) LastDigestAt() (time.Time, error) {
	return f.last, nil
}

func (f *fakeDigestStore) PendingForDigest(time.Time, time.Time) ([]store.EmailRow, error) {
	return f.rows, nil
}

func (f *fakeDigestStore) CompleteDigest([]int64, time.Time, time.Time, time.Time) error {
	f.completeCalls++
	return nil
}

type fakeMailer struct {
	err   error
	calls int
}

func (f *fakeMailer) Send(context.Context, string, string, string, string) error {
	f.calls++
	return f.err
}

func TestRunLockSkipsOverlap(t *testing.T) {
	p := &fakePoller{started: make(chan struct{}), release: make(chan struct{})}
	s := New(Config{}, p, &fakeDigestStore{}, &fakeMailer{})

	done := make(chan bool, 1)
	go func() { done <- s.RunPoll(context.Background()) }()
	<-p.started

	if ran := s.RunDigest(context.Background()); ran {
		t.Fatal("overlapping digest ran; want skipped")
	}
	close(p.release)
	if ran := <-done; !ran {
		t.Fatal("first poll was skipped")
	}
}

func TestStartRunsOnePollAndNoDigest(t *testing.T) {
	p := &fakePoller{started: make(chan struct{})}
	m := &fakeMailer{}
	s := New(Config{
		PollSchedule:   "0 0 1 1 *",
		DigestSchedule: "0 0 1 1 *",
		Location:       time.UTC,
	}, p, &fakeDigestStore{}, m)

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-p.started
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.calls.Load() != 1 || m.calls != 0 {
		t.Fatalf("poll/digest calls = %d/%d, want 1/0", p.calls.Load(), m.calls)
	}
}

func TestFailedSendLeavesRowsPending(t *testing.T) {
	now := time.Date(2026, time.June, 8, 8, 0, 0, 0, time.UTC)
	ds := &fakeDigestStore{
		last: now.AddDate(0, 0, -7),
		rows: []store.EmailRow{{ID: 1, ReceivedAt: now.Add(-time.Hour), InScope: true}},
	}
	m := &fakeMailer{err: errors.New("SMTP down")}
	s := New(Config{DigestTo: "owner@example.test", Location: time.UTC}, &fakePoller{}, ds, m)
	s.now = func() time.Time { return now }

	if ran := s.RunDigest(context.Background()); !ran {
		t.Fatal("digest was unexpectedly skipped")
	}
	if m.calls != 1 || ds.completeCalls != 0 {
		t.Fatalf("send/complete calls = %d/%d, want 1/0", m.calls, ds.completeCalls)
	}
}

func TestSuccessfulEmptyDigestAdvancesWindow(t *testing.T) {
	now := time.Date(2026, time.June, 8, 8, 0, 0, 0, time.UTC)
	ds := &fakeDigestStore{}
	m := &fakeMailer{}
	s := New(Config{DigestTo: "owner@example.test", Location: time.UTC}, &fakePoller{}, ds, m)
	s.now = func() time.Time { return now }

	if ran := s.RunDigest(context.Background()); !ran {
		t.Fatal("digest was unexpectedly skipped")
	}
	if m.calls != 1 || ds.completeCalls != 1 {
		t.Fatalf("send/complete calls = %d/%d, want 1/1", m.calls, ds.completeCalls)
	}
}
