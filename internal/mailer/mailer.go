// Package mailer sends multipart digest messages over SMTP.
package mailer

import (
	"context"
	"fmt"
	"time"

	mail "github.com/wneessen/go-mail"
)

// Config contains SMTP connection and sender settings.
type Config struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

type sender interface {
	DialAndSendWithContext(context.Context, ...*mail.Msg) error
}

// Mailer sends digest messages.
type Mailer struct {
	from   string
	sender sender
}

// New constructs an SMTP mailer requiring STARTTLS.
func New(cfg Config) (*Mailer, error) {
	client, err := mail.NewClient(
		cfg.Host,
		mail.WithPort(cfg.Port),
		mail.WithSMTPAuth(mail.SMTPAuthAutoDiscover),
		mail.WithUsername(cfg.User),
		mail.WithPassword(cfg.Pass),
		mail.WithTLSPortPolicy(mail.TLSMandatory),
		mail.WithTimeout(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("create SMTP client: %w", err)
	}
	return &Mailer{from: cfg.From, sender: client}, nil
}

// Send sends text and HTML alternatives as one message.
func (m *Mailer) Send(
	ctx context.Context,
	to, subject, htmlBody, textBody string,
) error {
	message := mail.NewMsg()
	if err := message.From(m.from); err != nil {
		return fmt.Errorf("set digest sender: %w", err)
	}
	if err := message.To(to); err != nil {
		return fmt.Errorf("set digest recipient: %w", err)
	}
	message.Subject(subject)
	message.SetBodyString(mail.TypeTextPlain, textBody)
	message.AddAlternativeString(mail.TypeTextHTML, htmlBody)

	if err := m.sender.DialAndSendWithContext(ctx, message); err != nil {
		return fmt.Errorf("send digest: %w", err)
	}
	return nil
}
