// Package poll orchestrates the sequential mailbox processing pipeline.
package poll

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/gbourcier/suzie/internal/archive"
	mailimap "github.com/gbourcier/suzie/internal/imap"
	"github.com/gbourcier/suzie/internal/llm"
	"github.com/gbourcier/suzie/internal/model"
	"github.com/gbourcier/suzie/internal/parse"
	"github.com/gbourcier/suzie/internal/store"
)

const failedSummary = "could not summarize - read original"

// Config controls message processing.
type Config struct {
	Folder          string
	ArchiveDir      string
	Allowlist       []string
	BodyCharLimit   int
	SummaryLanguage string
	LLMTimeout      time.Duration
}

type stateStore interface {
	LastUID(string) (uint32, uint32, error)
	SetLastUID(string, uint32, uint32) error
	EmailExists(uint32, uint32, string) (bool, error)
	InsertEmail(store.EmailRow) (bool, error)
}

type fetcher interface {
	Fetch(context.Context, uint32, uint32) (mailimap.FetchResult, error)
	MarkSeen(context.Context, uint32, uint32) error
}

type summarizer interface {
	Summarize(context.Context, llm.Input) (model.Summary, llm.RawDebug)
}

type archiveFunc func(string, time.Time, string, uint32, []byte) (string, error)
type parseFunc func([]byte, int) (model.Parsed, error)

// Job processes all currently available messages strictly one at a time.
type Job struct {
	store     stateStore
	fetcher   fetcher
	llm       summarizer
	cfg       Config
	archive   archiveFunc
	parse     parseFunc
	now       func() time.Time
	allowlist map[string]struct{}
}

// Result summarizes one poll run.
type Result struct {
	Initialized bool
	Fetched     int
	Processed   int
	Duplicates  int
	Errors      int
	OutOfScope  int
}

// New constructs a poll job.
func New(s stateStore, f fetcher, l summarizer, cfg Config) *Job {
	allowlist := make(map[string]struct{}, len(cfg.Allowlist))
	for _, addr := range cfg.Allowlist {
		addr = strings.ToLower(strings.TrimSpace(addr))
		if addr != "" {
			allowlist[addr] = struct{}{}
		}
	}
	return &Job{
		store:     s,
		fetcher:   f,
		llm:       l,
		cfg:       cfg,
		archive:   archive.Write,
		parse:     parse.Message,
		now:       time.Now,
		allowlist: allowlist,
	}
}

// Run fetches and durably processes every new message in UID order.
func (j *Job) Run(ctx context.Context) (Result, error) {
	knownValidity, lastUID, err := j.store.LastUID(j.cfg.Folder)
	if err != nil {
		return Result{}, fmt.Errorf("read poll cursor: %w", err)
	}
	fetched, err := j.fetcher.Fetch(ctx, knownValidity, lastUID)
	if err != nil {
		return Result{}, err
	}
	if fetched.Baseline {
		if err := j.store.SetLastUID(
			j.cfg.Folder,
			fetched.UIDValidity,
			fetched.LastUID,
		); err != nil {
			return Result{}, fmt.Errorf("save mailbox baseline: %w", err)
		}
		slog.Info("mailbox baseline initialized; existing messages skipped",
			"folder", j.cfg.Folder,
			"uid_validity", fetched.UIDValidity,
			"last_uid", fetched.LastUID,
		)
		return Result{Initialized: true}, nil
	}
	sort.Slice(fetched.Messages, func(a, b int) bool {
		return fetched.Messages[a].UID < fetched.Messages[b].UID
	})

	result := Result{Fetched: len(fetched.Messages)}
	for _, message := range fetched.Messages {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		outcome, err := j.processOne(ctx, fetched.UIDValidity, message)
		if err != nil {
			return result, fmt.Errorf("process UID %d: %w", message.UID, err)
		}
		result.Processed += outcome.processed
		result.Duplicates += outcome.duplicate
		result.Errors += outcome.failed
		result.OutOfScope += outcome.outOfScope
	}
	return result, nil
}

type messageOutcome struct {
	processed  int
	duplicate  int
	failed     int
	outOfScope int
}

func (j *Job) processOne(
	ctx context.Context,
	uidValidity uint32,
	message mailimap.RawMessage,
) (messageOutcome, error) {
	processedAt := j.now()
	parsed, parseErr := j.parse(message.Raw, j.cfg.BodyCharLimit)
	receivedAt := parsed.Date
	if receivedAt.IsZero() {
		receivedAt = processedAt
	}

	rawPath, err := j.archive(
		j.cfg.ArchiveDir,
		receivedAt,
		parsed.MessageID,
		message.UID,
		message.Raw,
	)
	if err != nil {
		return messageOutcome{}, fmt.Errorf("archive: %w", err)
	}

	exists, err := j.store.EmailExists(uidValidity, message.UID, parsed.MessageID)
	if err != nil {
		return messageOutcome{}, err
	}
	if exists {
		if err := j.fetcher.MarkSeen(ctx, uidValidity, message.UID); err != nil {
			return messageOutcome{}, err
		}
		if err := j.store.SetLastUID(j.cfg.Folder, uidValidity, message.UID); err != nil {
			return messageOutcome{}, err
		}
		slog.Info("email already processed", "imap_uid", message.UID)
		return messageOutcome{duplicate: 1}, nil
	}

	row := store.EmailRow{
		UIDValidity: uidValidity,
		IMAPUID:     message.UID,
		MessageID:   parsed.MessageID,
		ReceivedAt:  receivedAt,
		FromName:    parsed.From.Name,
		FromAddr:    parsed.From.Addr,
		Subject:     parsed.Subject,
		RawPath:     rawPath,
		InScope:     true,
		ProcessedAt: processedAt,
	}

	outcome := messageOutcome{processed: 1}
	switch {
	case parseErr != nil:
		row.Summary = failedSummary
		row.LLMStatus = "error"
		row.LLMError = "parse message: " + parseErr.Error()
		outcome.failed = 1
	case !j.inScope(parsed.From.Addr):
		row.Summary = "out of scope - not summarized"
		row.ActionReq = "none"
		row.Tone = "neutral"
		row.InScope = false
		row.LLMStatus = "ok"
		outcome.outOfScope = 1
	default:
		messageCtx, cancel := context.WithTimeout(ctx, j.cfg.LLMTimeout)
		summary, _ := j.llm.Summarize(messageCtx, llm.Input{
			From:     formatAddress(parsed.From),
			Subject:  parsed.Subject,
			Body:     parsed.BodyText,
			Language: j.cfg.SummaryLanguage,
		})
		cancel()
		row.Summary = summary.Text
		row.ActionReq = summary.Action
		row.Deadline = summary.Deadline
		row.Tone = summary.Tone
		row.LLMStatus = summary.Status
		row.LLMError = summary.Err
		if summary.Status == "error" {
			outcome.failed = 1
		}
	}

	inserted, err := j.store.InsertEmail(row)
	if err != nil {
		return messageOutcome{}, err
	}
	if !inserted {
		outcome.processed = 0
		outcome.duplicate = 1
	}
	if err := j.fetcher.MarkSeen(ctx, uidValidity, message.UID); err != nil {
		return messageOutcome{}, err
	}
	if err := j.store.SetLastUID(j.cfg.Folder, uidValidity, message.UID); err != nil {
		return messageOutcome{}, err
	}

	slog.Info("email processed",
		"imap_uid", message.UID,
		"message_id", parsed.MessageID,
		"status", row.LLMStatus,
		"in_scope", row.InScope,
	)
	return outcome, nil
}

func (j *Job) inScope(address string) bool {
	if len(j.allowlist) == 0 {
		return true
	}
	_, ok := j.allowlist[strings.ToLower(strings.TrimSpace(address))]
	return ok
}

func formatAddress(address model.Address) string {
	if address.Name == "" {
		return address.Addr
	}
	return fmt.Sprintf("%s <%s>", address.Name, address.Addr)
}
