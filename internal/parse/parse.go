package parse

import (
	"bytes"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jhillyerd/enmime"

	"github.com/gbourcier/suzie/internal/model"
)

// Message parses a raw .eml into a Parsed struct.
// charLimit caps BodyText in runes; 0 means no limit.
func Message(raw []byte, charLimit int) (model.Parsed, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return model.Parsed{}, err
	}

	var p model.Parsed

	// From
	fromStr := env.GetHeader("From")
	if addrs, err := mail.ParseAddressList(fromStr); err == nil && len(addrs) > 0 {
		p.From = model.Address{Name: addrs[0].Name, Addr: addrs[0].Address}
	} else {
		p.From = model.Address{Addr: strings.TrimSpace(fromStr)}
	}

	// Subject (enmime decodes MIME-encoded words automatically)
	p.Subject = env.GetHeader("Subject")

	// Message-ID: strip angle brackets
	mid := env.GetHeader("Message-Id")
	p.MessageID = strings.Trim(mid, "<> \t")

	// Date header (best-effort; ingest stage overrides with IMAP INTERNALDATE)
	if dateStr := env.GetHeader("Date"); dateStr != "" {
		if t, err := mail.ParseDate(dateStr); err == nil {
			p.Date = t
		}
	}

	// Body: prefer plaintext; fallback to stripped HTML
	body := env.Text
	if body == "" && env.HTML != "" {
		body = stripHTML(env.HTML)
	}

	body = normalizeWhitespace(body)
	body = StripQuotedText(body)

	// Truncate to rune limit
	if charLimit > 0 && utf8.RuneCountInString(body) > charLimit {
		runes := []rune(body)
		p.BodyText = string(runes[:charLimit])
		p.Truncated = true
	} else {
		p.BodyText = body
	}

	// HasAttachment: explicit attachments + inline parts with filenames
	p.HasAttachment = len(env.Attachments) > 0
	if !p.HasAttachment {
		for _, part := range env.Inlines {
			if part.FileName != "" {
				p.HasAttachment = true
				break
			}
		}
	}

	return p, nil
}

// stripHTML removes HTML markup and decodes common entities.
// Used only when a message has no text/plain part.
func stripHTML(html string) string {
	// Remove script and style content entirely
	scriptRe := regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	html = scriptRe.ReplaceAllString(html, " ")

	// Replace block-level elements with newlines
	blockRe := regexp.MustCompile(`(?i)<(br|p|div|tr|li|h[1-6]|blockquote)[^>]*/?>`)
	html = blockRe.ReplaceAllString(html, "\n")

	// Strip remaining tags
	tagRe := regexp.MustCompile(`<[^>]+>`)
	html = tagRe.ReplaceAllString(html, "")

	// Common HTML entities
	html = strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&nbsp;", " ",
		"&mdash;", "—",
		"&ndash;", "–",
		"&laquo;", "«",
		"&raquo;", "»",
	).Replace(html)

	return html
}

// normalizeWhitespace collapses whitespace: tabs/multiple-spaces on a line → single
// space, more than two consecutive blank lines → one blank line.
func normalizeWhitespace(s string) string {
	spaceRe := regexp.MustCompile(`[ \t]+`)
	multiBlankRe := regexp.MustCompile(`\n{3,}`)

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(spaceRe.ReplaceAllString(line, " "), " ")
	}
	result := strings.Join(lines, "\n")
	result = multiBlankRe.ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}

// quoteHeaderEN matches "On <date/name info> wrote:" on a single line.
var quoteHeaderEN = regexp.MustCompile(`(?i)^On .+wrote:\s*$`)

// quoteHeaderFR matches "Le <date/name info> a écrit :" on a single line.
var quoteHeaderFR = regexp.MustCompile(`(?i)^Le .+a [eé]crit\s*:\s*$`)

func isQuoteHeader(line string) bool {
	return quoteHeaderEN.MatchString(line) || quoteHeaderFR.MatchString(line)
}

// StripQuotedText removes quoted reply sections and trailing signatures.
// It is exported so tests can cover it directly (as required by M1 AC).
//
// Conservatism: only lines starting with ">" are dropped, and quote headers
// are only dropped when the very next non-empty line is itself quoted.
// This preserves interleaved reply content (text between two quote blocks).
func StripQuotedText(body string) string {
	lines := strings.Split(body, "\n")

	// Cut at signature delimiter ("--" on its own line, with optional trailing spaces)
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "--" {
			lines = lines[:i]
			break
		}
	}

	result := make([]string, 0, len(lines))
	for i, line := range lines {
		// Drop lines that begin a quoted block (the ">" character)
		if strings.HasPrefix(line, ">") {
			continue
		}

		// Drop a quote header only when the next non-empty line is actually quoted
		if isQuoteHeader(line) {
			nextIsQuoted := false
			for j := i + 1; j < len(lines); j++ {
				if lines[j] == "" {
					continue
				}
				nextIsQuoted = strings.HasPrefix(lines[j], ">")
				break
			}
			if nextIsQuoted {
				continue
			}
		}

		result = append(result, line)
	}

	return strings.TrimSpace(strings.Join(result, "\n"))
}
