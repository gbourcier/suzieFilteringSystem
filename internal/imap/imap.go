// Package imap fetches complete messages and marks processed mail as seen.
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

// FetchResult describes either a new-mail batch or a high-water baseline.
type FetchResult struct {
	UIDValidity uint32
	LastUID     uint32
	Baseline    bool
	Messages    []RawMessage
}

// Fetcher opens one IMAP connection per operation.
type Fetcher struct {
	cfg  Config
	dial dialFunc
}

type dialFunc func(context.Context) (session, error)

type session interface {
	Login(string, string) error
	Select(string, bool) (mailboxState, error)
	FetchSince(uint32) ([]RawMessage, error)
	MarkSeen(uint32) error
	Logout() error
}

type mailboxState struct {
	UIDValidity uint32
	UIDNext     uint32
}

// New constructs a Fetcher.
func New(cfg Config) *Fetcher {
	f := &Fetcher{cfg: cfg}
	f.dial = f.dialSession
	return f
}

// Fetch returns only messages newer than sinceUID. On first use or after a
// UIDVALIDITY change, it returns a baseline at UIDNEXT-1 without fetching mail.
func (f *Fetcher) Fetch(
	ctx context.Context,
	knownUIDValidity, sinceUID uint32,
) (FetchResult, error) {
	if err := ctx.Err(); err != nil {
		return FetchResult{}, err
	}
	s, err := f.dial(ctx)
	if err != nil {
		return FetchResult{}, fmt.Errorf("connect IMAP: %w", err)
	}
	defer func() { _ = s.Logout() }()

	if err := s.Login(f.cfg.User, f.cfg.Pass); err != nil {
		return FetchResult{}, fmt.Errorf("IMAP login: %w", err)
	}
	state, err := s.Select(f.cfg.Folder, true)
	if err != nil {
		return FetchResult{}, fmt.Errorf("select IMAP folder %q: %w", f.cfg.Folder, err)
	}

	if knownUIDValidity == 0 || knownUIDValidity != state.UIDValidity {
		return FetchResult{
			UIDValidity: state.UIDValidity,
			LastUID:     latestUID(state.UIDNext),
			Baseline:    true,
		}, nil
	}
	if sinceUID == math.MaxUint32 {
		return FetchResult{UIDValidity: state.UIDValidity, LastUID: sinceUID}, nil
	}

	start := sinceUID + 1
	msgs, err := s.FetchSince(start)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch IMAP messages: %w", err)
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].UID < msgs[j].UID })
	return FetchResult{
		UIDValidity: state.UIDValidity,
		LastUID:     sinceUID,
		Messages:    msgs,
	}, nil
}

// MarkSeen adds the \Seen flag to uid after verifying the UIDVALIDITY.
func (f *Fetcher) MarkSeen(ctx context.Context, expectedUIDValidity, uid uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := f.dial(ctx)
	if err != nil {
		return fmt.Errorf("connect IMAP: %w", err)
	}
	defer func() { _ = s.Logout() }()

	if err := s.Login(f.cfg.User, f.cfg.Pass); err != nil {
		return fmt.Errorf("IMAP login: %w", err)
	}
	state, err := s.Select(f.cfg.Folder, false)
	if err != nil {
		return fmt.Errorf("select IMAP folder %q: %w", f.cfg.Folder, err)
	}
	if state.UIDValidity != expectedUIDValidity {
		return fmt.Errorf(
			"UIDVALIDITY changed before marking UID %d seen: got %d, want %d",
			uid,
			state.UIDValidity,
			expectedUIDValidity,
		)
	}
	if err := s.MarkSeen(uid); err != nil {
		return fmt.Errorf("mark UID %d seen: %w", uid, err)
	}
	return nil
}

func latestUID(uidNext uint32) uint32 {
	if uidNext == 0 {
		return 0
	}
	return uidNext - 1
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

func (s *imapSession) Select(folder string, readOnly bool) (mailboxState, error) {
	status, err := s.client.Select(folder, readOnly)
	if err != nil {
		return mailboxState{}, err
	}
	return mailboxState{UIDValidity: status.UidValidity, UIDNext: status.UidNext}, nil
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

func (s *imapSession) MarkSeen(uid uint32) error {
	seqSet := new(emersionimap.SeqSet)
	seqSet.AddNum(uid)
	item := emersionimap.FormatFlagsOp(emersionimap.AddFlags, true)
	return s.client.UidStore(
		seqSet,
		item,
		[]interface{}{emersionimap.SeenFlag},
		nil,
	)
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
