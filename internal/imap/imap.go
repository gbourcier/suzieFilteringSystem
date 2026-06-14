// Package imap fetches complete messages from a mailbox without mutating it.
package imap

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"time"

	emersionimap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const defaultCommandTimeout = 5 * time.Minute

// Config contains the connection settings needed by Fetcher.
type Config struct {
	Host   string
	Port   int
	User   string
	Pass   string
	Folder string
}

// RawMessage is a complete RFC 822 source message and its mailbox UID.
type RawMessage struct {
	UID uint32
	Raw []byte
}

// Fetcher opens one read-only IMAP connection per Fetch call.
type Fetcher struct {
	cfg  Config
	dial dialFunc
}

type dialFunc func(context.Context) (session, error)

type session interface {
	Login(string, string) error
	Select(string) (uint32, error)
	FetchSince(uint32) ([]RawMessage, error)
	Logout() error
}

// New constructs a Fetcher.
func New(cfg Config) *Fetcher {
	f := &Fetcher{cfg: cfg}
	f.dial = f.dialSession
	return f
}

// Fetch returns messages newer than sinceUID, or all messages after UIDVALIDITY changes.
func (f *Fetcher) Fetch(
	ctx context.Context,
	knownUIDValidity, sinceUID uint32,
) (uint32, []RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	s, err := f.dial(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("connect IMAP: %w", err)
	}
	defer func() { _ = s.Logout() }()

	if err := s.Login(f.cfg.User, f.cfg.Pass); err != nil {
		return 0, nil, fmt.Errorf("IMAP login: %w", err)
	}
	uidValidity, err := s.Select(f.cfg.Folder)
	if err != nil {
		return 0, nil, fmt.Errorf("select IMAP folder %q: %w", f.cfg.Folder, err)
	}

	start := firstUID(knownUIDValidity, uidValidity, sinceUID)
	if start == 0 {
		return uidValidity, nil, nil
	}
	msgs, err := s.FetchSince(start)
	if err != nil {
		return 0, nil, fmt.Errorf("fetch IMAP messages: %w", err)
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].UID < msgs[j].UID })
	return uidValidity, msgs, nil
}

func firstUID(known, current, since uint32) uint32 {
	if known != 0 && known != current {
		return 1
	}
	if since == math.MaxUint32 {
		return 0
	}
	return since + 1
}

func (f *Fetcher) dialSession(ctx context.Context) (session, error) {
	addr := net.JoinHostPort(f.cfg.Host, fmt.Sprintf("%d", f.cfg.Port))
	timeout := commandTimeout(ctx)
	dialer := &contextDialer{
		ctx: ctx,
		dialer: net.Dialer{
			Timeout: timeout,
		},
	}
	c, err := client.DialWithDialerTLS(dialer, addr, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: f.cfg.Host,
	})
	if err != nil {
		return nil, err
	}
	c.Timeout = timeout
	return &imapSession{client: c}, nil
}

type contextDialer struct {
	ctx    context.Context
	dialer net.Dialer
}

func (d *contextDialer) Dial(network, address string) (net.Conn, error) {
	conn, err := d.dialer.DialContext(d.ctx, network, address)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(commandTimeout(d.ctx))); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

type imapSession struct {
	client *client.Client
}

func (s *imapSession) Login(user, pass string) error {
	return s.client.Login(user, pass)
}

func (s *imapSession) Select(folder string) (uint32, error) {
	status, err := s.client.Select(folder, true)
	if err != nil {
		return 0, err
	}
	return status.UidValidity, nil
}

func (s *imapSession) FetchSince(first uint32) ([]RawMessage, error) {
	seqSet := new(emersionimap.SeqSet)
	seqSet.AddRange(first, 0)
	section := &emersionimap.BodySectionName{Peek: true}
	items := []emersionimap.FetchItem{emersionimap.FetchUid, section.FetchItem()}
	messages := make(chan *emersionimap.Message, 16)
	done := make(chan error, 1)
	go func() {
		done <- s.client.UidFetch(seqSet, items, messages)
	}()

	var result []RawMessage
	var bodyErr error
	for msg := range messages {
		if msg == nil || msg.Uid < first {
			continue
		}
		body := msg.GetBody(section)
		if body == nil {
			if bodyErr == nil {
				bodyErr = fmt.Errorf("UID %d has no RFC822 body", msg.Uid)
			}
			continue
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			if bodyErr == nil {
				bodyErr = fmt.Errorf("read UID %d body: %w", msg.Uid, err)
			}
			continue
		}
		result = append(result, RawMessage{UID: msg.Uid, Raw: raw})
	}
	if err := <-done; err != nil {
		return nil, err
	}
	if bodyErr != nil {
		return nil, bodyErr
	}
	return result, nil
}

func (s *imapSession) Logout() error {
	return s.client.Logout()
}

func commandTimeout(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return defaultCommandTimeout
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	if remaining < defaultCommandTimeout {
		return remaining
	}
	return defaultCommandTimeout
}
