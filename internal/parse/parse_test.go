package parse

import (
	"os"
	"strings"
	"testing"
	"time"
)

// loadFixture reads a file from ../../testdata relative to this package.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return data
}

func TestMessage_BasicFr(t *testing.T) {
	raw := loadFixture(t, "basic_fr.eml")
	p, err := Message(raw, 0)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}

	if p.From.Addr != "board@condo.tld" {
		t.Errorf("From.Addr = %q, want %q", p.From.Addr, "board@condo.tld")
	}
	if p.From.Name != "Syndicat de copropriété" {
		t.Errorf("From.Name = %q, want %q", p.From.Name, "Syndicat de copropriété")
	}
	if p.Subject != "Avis de cotisation spéciale" {
		t.Errorf("Subject = %q", p.Subject)
	}
	if p.MessageID != "avis-20250512@condo.tld" {
		t.Errorf("MessageID = %q", p.MessageID)
	}
	if p.Date.IsZero() {
		t.Error("Date should not be zero")
	}
	wantYear := 2025
	if p.Date.Year() != wantYear {
		t.Errorf("Date year = %d, want %d", p.Date.Year(), wantYear)
	}
	if p.BodyText == "" {
		t.Error("BodyText should not be empty")
	}
	if !strings.Contains(p.BodyText, "cotisation spéciale") {
		t.Errorf("BodyText does not contain expected content: %q", p.BodyText)
	}
	if p.HasAttachment {
		t.Error("HasAttachment should be false")
	}
	if p.Truncated {
		t.Error("Truncated should be false when charLimit=0")
	}
}

func TestMessage_WithAttachment(t *testing.T) {
	raw := loadFixture(t, "with_attachment.eml")
	p, err := Message(raw, 0)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}

	if !p.HasAttachment {
		t.Error("HasAttachment should be true")
	}
	if p.BodyText == "" {
		t.Error("BodyText should not be empty")
	}
	if !strings.Contains(p.BodyText, "procès-verbal") {
		t.Errorf("BodyText does not contain expected content: %q", p.BodyText)
	}
}

func TestMessage_Truncation(t *testing.T) {
	raw := loadFixture(t, "basic_fr.eml")
	limit := 20
	p, err := Message(raw, limit)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}

	runes := []rune(p.BodyText)
	if len(runes) > limit {
		t.Errorf("BodyText has %d runes, want <= %d", len(runes), limit)
	}
	if !p.Truncated {
		t.Error("Truncated should be true")
	}
}

func TestMessage_NoTruncationWhenFit(t *testing.T) {
	raw := loadFixture(t, "basic_fr.eml")
	p, err := Message(raw, 10000)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if p.Truncated {
		t.Error("Truncated should be false when limit is large")
	}
}

func TestMessage_QuotedReply(t *testing.T) {
	raw := loadFixture(t, "quoted_reply.eml")
	p, err := Message(raw, 0)
	if err != nil {
		t.Fatalf("Message: %v", err)
	}

	// Quoted lines ("> ...") should be stripped
	if strings.Contains(p.BodyText, "infiltration") {
		t.Errorf("BodyText should not contain quoted text 'infiltration', got: %q", p.BodyText)
	}
	// Original reply content should be retained
	if !strings.Contains(p.BodyText, "inspection") {
		t.Errorf("BodyText should contain 'inspection', got: %q", p.BodyText)
	}
	// Signature should be stripped
	if strings.Contains(p.BodyText, "Syndicat de copropriété") {
		t.Errorf("BodyText should not contain signature, got: %q", p.BodyText)
	}
	// Content after the quote should be retained (interleaved reply)
	if !strings.Contains(p.BodyText, "5 jours ouvrables") {
		t.Errorf("BodyText should contain post-quote content '5 jours ouvrables', got: %q", p.BodyText)
	}
}

// TestStripQuotedText covers the quote-stripping helper directly.
func TestStripQuotedText(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantIn  []string // substrings that should be present
		wantOut []string // substrings that should be absent
	}{
		{
			name: "no quotes",
			input: `Hello world.
This is a plain message.`,
			wantIn: []string{"Hello world.", "plain message"},
		},
		{
			name: "quoted block stripped",
			input: `My reply here.

On Mon, 1 Jun 2025, Board wrote:
> Original message line 1
> Original message line 2`,
			wantIn:  []string{"My reply here."},
			wantOut: []string{"Original message line 1"},
		},
		{
			name: "french quote header stripped",
			input: `Ma réponse ici.

Le 1 juin 2025, Syndicat <board@condo.tld> a écrit :
> Texte original`,
			wantIn:  []string{"Ma réponse ici."},
			wantOut: []string{"Texte original"},
		},
		{
			name: "signature stripped",
			input: `Main content here.

--
Signature block
Contact info`,
			wantIn:  []string{"Main content here."},
			wantOut: []string{"Signature block", "Contact info"},
		},
		{
			name: "interleaved reply: content after quote block preserved",
			input: `First response.

On Jun 1, Board wrote:
> Quoted text

Second response after the quote.`,
			wantIn:  []string{"First response.", "Second response after the quote."},
			wantOut: []string{"Quoted text"},
		},
		{
			name: "quote header NOT stripped when not followed by quoted line",
			input: `On June 1, someone wrote a new proposal:
The proposal content here.`,
			// This looks like a quote header but is followed by non-quoted text,
			// so it should be kept (conservative stripping).
			wantIn: []string{"On June 1, someone wrote a new proposal:", "The proposal content here."},
		},
		{
			name:    "empty input",
			input:   "",
			wantIn:  nil,
			wantOut: nil,
		},
		{
			name: "date in 2024",
			input: `Content.

On 2024-01-15 12:00, Sender wrote:
> Quoted line`,
			wantIn:  []string{"Content."},
			wantOut: []string{"Quoted line"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StripQuotedText(tc.input)
			for _, want := range tc.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("result %q should contain %q", got, want)
				}
			}
			for _, absent := range tc.wantOut {
				if strings.Contains(got, absent) {
					t.Errorf("result %q should NOT contain %q", got, absent)
				}
			}
		})
	}
}

func TestDateParsing(t *testing.T) {
	// Ensure the Date field from a well-formed header is parsed into the correct month.
	raw := loadFixture(t, "basic_fr.eml")
	p, err := Message(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Date.Month() != time.May {
		t.Errorf("Date.Month = %v, want May", p.Date.Month())
	}
}
