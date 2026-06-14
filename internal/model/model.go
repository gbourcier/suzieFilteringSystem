package model

import "time"

// Address is a sender name and email pair.
type Address struct {
	Name, Addr string
}

// Parsed is the output of internal/parse for one message.
type Parsed struct {
	MessageID     string
	From          Address
	Subject       string
	Date          time.Time // from Date header; may be zero if absent/unparseable
	BodyText      string    // cleaned plaintext, truncated to the char limit
	HasAttachment bool
	Truncated     bool
}

// Summary is the validated LLM result from internal/llm.
type Summary struct {
	Text     string // <=200 chars, neutral
	Action   string // none|reply|action|fyi
	Deadline string // "YYYY-MM-DD" or ""
	Tone     string // neutral|heated
	Status   string // ok|error
	Err      string // set when Status=="error"
}
