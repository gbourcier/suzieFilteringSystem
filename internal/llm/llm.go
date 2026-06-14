// Package llm provides a client for Ollama's /api/chat endpoint that produces
// structured email summaries.  Summarize always returns a persistable Summary
// and never propagates Go errors for LLM-level failures (fail-open design).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gbourcier/suzie/internal/model"
)

// Input is the data sent to the LLM for one message.
type Input struct {
	From     string
	Subject  string
	Body     string
	Language string // "fr" or "en"
}

// RawDebug carries diagnostic information from an LLM call (for the dev harness).
type RawDebug struct {
	RawResponse string
	LatencyMS   int64
	Retried     bool
}

// Client calls the Ollama /api/chat endpoint.
type Client struct {
	baseURL   string
	model     string
	httpCli   *http.Client
	keepAlive string
}

// New constructs a Client.  timeout is applied per HTTP call.
func New(baseURL, modelName string, timeout time.Duration) *Client {
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		model:     modelName,
		httpCli:   &http.Client{Timeout: timeout},
		keepAlive: "5m",
	}
}

// ollamaRequest is the JSON body for POST /api/chat.
type ollamaRequest struct {
	Model     string          `json:"model"`
	Messages  []ollamaMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	Format    json.RawMessage `json:"format"`
	Options   ollamaOptions   `json:"options"`
	KeepAlive string          `json:"keep_alive"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
	NumCtx      int     `json:"num_ctx"`
}

type ollamaResponse struct {
	Message ollamaMessage `json:"message"`
}

// llmOutput mirrors the JSON schema sent to Ollama.
type llmOutput struct {
	Summary        string  `json:"summary"`
	ActionRequired string  `json:"action_required"`
	Deadline       *string `json:"deadline"`
	Tone           string  `json:"tone"`
}

// outputSchema is the structured-output format constraint sent with every request.
var outputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary":          {"type":"string","maxLength":200},
    "action_required":  {"type":"string","enum":["none","reply","action","fyi"]},
    "deadline":         {"type":["string","null"]},
    "tone":             {"type":"string","enum":["neutral","heated"]}
  },
  "required":["summary","action_required","deadline","tone"]
}`)

const fallbackText = "could not summarize"

// Summarize sends the email to the LLM and returns a validated Summary.
// It retries once with a stricter prompt on any failure.  On second failure it
// returns a Summary with Status="error" — it never returns a Go error for
// LLM-level problems so the caller can always persist the row.
func (c *Client) Summarize(ctx context.Context, in Input) (model.Summary, RawDebug) {
	t0 := time.Now()
	raw, err := c.call(ctx, in, false)
	ms := time.Since(t0).Milliseconds()

	if err == nil {
		if s, vErr := validate(raw); vErr == nil {
			return s, RawDebug{RawResponse: raw, LatencyMS: ms}
		}
	}

	// Retry with stricter instruction
	t1 := time.Now()
	raw2, err2 := c.call(ctx, in, true)
	ms2 := time.Since(t1).Milliseconds()
	total := ms + ms2

	if err2 == nil {
		if s, vErr := validate(raw2); vErr == nil {
			return s, RawDebug{RawResponse: raw2, LatencyMS: total, Retried: true}
		}
	}

	errMsg := "validation failed after retry"
	if err2 != nil {
		errMsg = err2.Error()
	}
	return model.Summary{
		Text:   fallbackText,
		Status: "error",
		Err:    errMsg,
	}, RawDebug{RawResponse: raw2, LatencyMS: total, Retried: true}
}

// call makes one HTTP request to Ollama.
func (c *Client) call(ctx context.Context, in Input, strict bool) (string, error) {
	userContent := fmt.Sprintf("From: %s\nSubject: %s\n\n%s", in.From, in.Subject, in.Body)

	body, _ := json.Marshal(ollamaRequest{
		Model: c.model,
		Messages: []ollamaMessage{
			{Role: "system", Content: systemPrompt(in.Language, strict)},
			{Role: "user", Content: userContent},
		},
		Stream:    false,
		Format:    outputSchema,
		Options:   ollamaOptions{Temperature: 0, NumCtx: 8192},
		KeepAlive: c.keepAlive,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // error unrecoverable in defer

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}

	var ollamaResp ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return ollamaResp.Message.Content, nil
}

// validate parses the LLM JSON content and checks semantic constraints.
func validate(raw string) (model.Summary, error) {
	var out llmOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return model.Summary{}, fmt.Errorf("invalid JSON: %w", err)
	}

	if out.Summary == "" {
		return model.Summary{}, fmt.Errorf("empty summary")
	}

	// Truncate if the model exceeded the character limit despite the schema constraint.
	text := out.Summary
	if utf8.RuneCountInString(text) > 200 {
		runes := []rune(text)
		text = string(runes[:200])
	}

	validActions := map[string]bool{"none": true, "reply": true, "action": true, "fyi": true}
	if !validActions[out.ActionRequired] {
		return model.Summary{}, fmt.Errorf("invalid action_required: %q", out.ActionRequired)
	}

	validTones := map[string]bool{"neutral": true, "heated": true}
	if !validTones[out.Tone] {
		return model.Summary{}, fmt.Errorf("invalid tone: %q", out.Tone)
	}

	deadline := ""
	if out.Deadline != nil && *out.Deadline != "" {
		dl := *out.Deadline
		if _, err := time.Parse("2006-01-02", dl); err != nil {
			return model.Summary{}, fmt.Errorf("invalid deadline %q: %w", dl, err)
		}
		deadline = dl
	}

	return model.Summary{
		Text:     text,
		Action:   out.ActionRequired,
		Deadline: deadline,
		Tone:     out.Tone,
		Status:   "ok",
	}, nil
}

// systemPrompt builds the system message for the LLM.
func systemPrompt(language string, strict bool) string {
	outputLang := "French"
	if language == "en" {
		outputLang = "English"
	}

	p := fmt.Sprintf(`You are a neutral summarizer for condo-board correspondence written in Québec French.
For each email, produce one neutral, factual sentence in %s describing what the sender is asking for or stating.
Strip all emotional language, accusations, and rhetoric. Do not editorialize.
If no concrete request exists, summarize the main topic.

For the "action_required" field use exactly one of these values:
- "action": a resident or board member must do something physical (move a vehicle, write a cheque, clean a surface, attend a meeting, etc.)
- "reply": the sender explicitly asks for a written response or confirmation
- "fyi": informational only — no response or physical action is needed
- "none": not addressed to residents (internal board note, automated message, etc.)

For the "tone" field:
- "heated": the email contains personal accusations, insults, threats, aggressive demands, or describes a conflict between individuals
- "neutral": everything else

For the "deadline" field: extract only an explicit response-by or must-act-by date in YYYY-MM-DD format. Do NOT use event dates (meeting dates, inspection dates, scheduled work dates) as deadlines — those are not deadlines for action. Use null if no response deadline exists.

The summary sentence MUST be written in %s. Do not switch languages.
Output ONLY valid JSON matching the required schema.`, outputLang, outputLang)

	if strict {
		p += "\n\nCRITICAL: Respond with a JSON object ONLY. No preamble, no explanation, no markdown. The summary field MUST be in " + outputLang + "."
	}
	return p
}
