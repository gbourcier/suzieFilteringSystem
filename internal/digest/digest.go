// Package digest renders weekly email digests.
package digest

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/gbourcier/suzie/internal/store"
)

// View is the data rendered into both digest alternatives.
type View struct {
	WindowStart time.Time
	WindowEnd   time.Time
	Rows        []store.EmailRow
}

type renderView struct {
	Window string
	Rows   []renderRow
	Empty  bool
}

type renderRow struct {
	Date       string
	From       string
	Subject    string
	Summary    string
	Action     string
	Deadline   string
	Tone       string
	Original   string
	Heated     bool
	Error      bool
	OutOfScope bool
}

const htmlTemplate = `<!doctype html>
<html>
<body style="margin:0;background:#f4f5f7;color:#20242a;font-family:Arial,sans-serif;">
<div style="max-width:600px;margin:0 auto;padding:20px 12px;">
  <h1 style="font-size:22px;font-weight:600;margin:0 0 6px;">Weekly email digest</h1>
  <p style="color:#626a73;font-size:14px;margin:0 0 20px;">{{.Window}}</p>
  {{if .Empty}}
  <div style="background:#fff;border:1px solid #dde1e6;border-radius:10px;padding:18px;">
    No messages were received during this period.
  </div>
  {{else}}
  {{range .Rows}}
  <div style="background:#fff;border:1px solid #dde1e6;border-radius:10px;padding:16px;margin:0 0 14px;">
    <div style="color:#626a73;font-size:13px;margin-bottom:7px;">{{.Date}} | {{.From}}</div>
    <div style="font-size:16px;font-weight:600;margin-bottom:8px;">{{.Subject}}</div>
    <div style="font-size:15px;line-height:1.45;margin-bottom:12px;">{{.Summary}}</div>
    <div style="font-size:13px;line-height:1.8;">
      <span style="display:inline-block;background:#edf1f5;border-radius:12px;padding:1px 8px;margin-right:5px;">Action: {{.Action}}</span>
      {{if .Deadline}}<span style="display:inline-block;background:#fff4d6;border-radius:12px;padding:1px 8px;margin-right:5px;">Deadline: {{.Deadline}}</span>{{end}}
      {{if .Heated}}<span style="display:inline-block;background:#fbe4e4;color:#7b2d2d;border-radius:12px;padding:1px 8px;margin-right:5px;">Tone: heated</span>{{else}}<span style="display:inline-block;background:#edf1f5;border-radius:12px;padding:1px 8px;margin-right:5px;">Tone: {{.Tone}}</span>{{end}}
      {{if .Error}}<span style="display:inline-block;background:#fbe4e4;color:#7b2d2d;border-radius:12px;padding:1px 8px;">Summary error</span>{{end}}
      {{if .OutOfScope}}<span style="display:inline-block;background:#edf1f5;border-radius:12px;padding:1px 8px;">Out of scope</span>{{end}}
    </div>
    <div style="color:#626a73;font-size:12px;line-height:1.4;margin-top:12px;overflow-wrap:anywhere;">Original: {{.Original}}</div>
  </div>
  {{end}}
  {{end}}
</div>
</body>
</html>
`

const textTemplate = `Weekly email digest
{{.Window}}
{{if .Empty}}
No messages were received during this period.
{{else}}{{range .Rows}}
---
{{.Date}} | {{.From}}
{{.Subject}}
{{.Summary}}
Action: {{.Action}}{{if .Deadline}} | Deadline: {{.Deadline}}{{end}} | Tone: {{.Tone}}{{if .Error}} | Summary error{{end}}{{if .OutOfScope}} | Out of scope{{end}}
Original: {{.Original}}
{{end}}{{end}}`

// RenderHTML renders the mobile-friendly HTML alternative.
func RenderHTML(v View) (string, error) {
	tpl, err := template.New("digest").Parse(htmlTemplate)
	if err != nil {
		return "", fmt.Errorf("parse HTML digest template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, makeRenderView(v)); err != nil {
		return "", fmt.Errorf("render HTML digest: %w", err)
	}
	return buf.String(), nil
}

// RenderText renders the plaintext alternative.
func RenderText(v View) (string, error) {
	tpl, err := texttemplate.New("digest").Parse(textTemplate)
	if err != nil {
		return "", fmt.Errorf("parse text digest template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, makeRenderView(v)); err != nil {
		return "", fmt.Errorf("render text digest: %w", err)
	}
	return buf.String(), nil
}

func makeRenderView(v View) renderView {
	loc := v.WindowEnd.Location()
	if loc == nil {
		loc = time.UTC
	}
	result := renderView{
		Window: fmt.Sprintf(
			"%s to %s",
			v.WindowStart.In(loc).Format("Jan 2, 2006 15:04 MST"),
			v.WindowEnd.In(loc).Format("Jan 2, 2006 15:04 MST"),
		),
		Empty: len(v.Rows) == 0,
		Rows:  make([]renderRow, 0, len(v.Rows)),
	}
	for _, row := range v.Rows {
		from := row.FromAddr
		if row.FromName != "" {
			from = fmt.Sprintf("%s <%s>", row.FromName, row.FromAddr)
		}
		if from == "" {
			from = "(unknown sender)"
		}
		subject := strings.TrimSpace(row.Subject)
		if subject == "" {
			subject = "(no subject)"
		}
		summary := strings.TrimSpace(row.Summary)
		if summary == "" {
			summary = "could not summarize - read original"
		}
		action := row.ActionReq
		if action == "" {
			action = "none"
		}
		tone := row.Tone
		if tone == "" {
			tone = "neutral"
		}
		result.Rows = append(result.Rows, renderRow{
			Date:       row.ReceivedAt.In(loc).Format("Jan 2, 2006 15:04"),
			From:       from,
			Subject:    subject,
			Summary:    summary,
			Action:     action,
			Deadline:   row.Deadline,
			Tone:       tone,
			Original:   row.RawPath,
			Heated:     tone == "heated",
			Error:      row.LLMStatus == "error",
			OutOfScope: !row.InScope,
		})
	}
	return result
}
