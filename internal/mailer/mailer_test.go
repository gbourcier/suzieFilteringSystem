package mailer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	mail "github.com/wneessen/go-mail"
)

type fakeSender struct {
	err  error
	data string
}

func (f *fakeSender) DialAndSendWithContext(_ context.Context, messages ...*mail.Msg) error {
	if f.err != nil {
		return f.err
	}
	var buf bytes.Buffer
	if _, err := messages[0].Write(&buf); err != nil {
		return err
	}
	f.data = buf.String()
	return nil
}

func TestSendBuildsMultipartAlternative(t *testing.T) {
	transport := &fakeSender{}
	m := &Mailer{from: "digest@example.test", sender: transport}

	err := m.Send(
		context.Background(),
		"owner@example.test",
		"Weekly digest",
		"<p>HTML digest</p>",
		"Text digest",
	)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, want := range []string{
		"multipart/alternative",
		"Weekly digest",
		"Text digest",
		"HTML digest",
	} {
		if !strings.Contains(transport.data, want) {
			t.Fatalf("serialized message missing %q:\n%s", want, transport.data)
		}
	}
}

func TestSendPropagatesTransportError(t *testing.T) {
	m := &Mailer{
		from:   "digest@example.test",
		sender: &fakeSender{err: errors.New("SMTP unavailable")},
	}
	if err := m.Send(
		context.Background(),
		"owner@example.test",
		"Weekly digest",
		"<p>HTML</p>",
		"Text",
	); err == nil {
		t.Fatal("Send returned nil error")
	}
}
