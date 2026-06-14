// cmd/devsummarize is a developer harness for testing the parse → summarize
// pipeline against local .eml files without any Docker, IMAP, SMTP, or SQLite
// dependency.  It is excluded from the production image (see Dockerfile and
// .dockerignore).  See docs/SUMMARIZATION_PIPELINE_PLAN.md for full spec.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gbourcier/suzie/internal/llm"
	"github.com/gbourcier/suzie/internal/model"
	"github.com/gbourcier/suzie/internal/parse"
)

var (
	flagFixtures    = flag.String("fixtures", "./.fixtures", "dir of .eml (recurses); or a single .eml/.txt path")
	flagOllamaURL   = flag.String("ollama-url", "http://localhost:11434", "host Ollama endpoint")
	flagModel       = flag.String("model", "qwen2.5:14b", "Ollama model tag")
	flagLanguage    = flag.String("language", "fr", "summary output language (fr|en)")
	flagBodyLimit   = flag.Int("body-char-limit", 4000, "truncation budget passed to parse.Message")
	flagTimeout     = flag.Duration("timeout", 10*time.Minute, "per-message LLM timeout")
	flagOut         = flag.String("out", "", "write Markdown report to this path")
	flagJSON        = flag.Bool("json", false, "emit JSON Lines instead of human report")
	flagShowBody    = flag.Bool("show-body", false, "include cleaned body in the human report")
	flagFailInvalid = flag.Bool("fail-on-invalid", false, "exit non-zero if any message has validation warnings")
)

type record struct {
	File      string         `json:"file"`
	From      string         `json:"from"`
	Subject   string         `json:"subject"`
	HasAttach bool           `json:"has_attachment"`
	Truncated bool           `json:"truncated"`
	Summary   model.Summary  `json:"summary"`
	Debug     llm.RawDebug   `json:"debug"`
	Checks    []string       `json:"checks"`
	Body      string         `json:"body,omitempty"`
	ParseErr  string         `json:"parse_error,omitempty"`
}

func main() {
	flag.Parse()

	files, err := resolveFiles(*flagFixtures)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no .eml or .txt files found under %s\n", *flagFixtures)
		fmt.Fprintf(os.Stderr, "export real messages as .eml into %s (gitignored)\n", *flagFixtures)
		os.Exit(1)
	}

	if err := preflight(*flagOllamaURL, *flagModel); err != nil {
		fmt.Fprintf(os.Stderr, "preflight: %v\n", err)
		os.Exit(1)
	}

	client := llm.New(*flagOllamaURL, *flagModel, *flagTimeout)
	sort.Strings(files)

	records := make([]record, 0, len(files))
	var nOK, nErr, nWarn int
	var totalMS int64

	for _, f := range files {
		r := processFile(f, client)
		records = append(records, r)

		switch {
		case r.ParseErr != "":
			nErr++
		case r.Summary.Status == "error":
			nErr++
		default:
			nOK++
		}
		if len(r.Checks) > 0 {
			nWarn++
		}
		totalMS += r.Debug.LatencyMS

		if *flagJSON {
			printJSON(r)
		} else {
			printHuman(r, *flagShowBody)
		}
	}

	n := len(records)
	meanMS := int64(0)
	if n > 0 {
		meanMS = totalMS / int64(n)
	}

	if *flagJSON {
		b, _ := json.Marshal(map[string]any{
			"total": n, "ok": nOK, "error": nErr, "validation_warn": nWarn,
			"total_latency_ms": totalMS, "mean_latency_ms": meanMS,
		})
		fmt.Println(string(b))
	} else {
		fmt.Printf("\n── Summary ────────────────────────────────────────────────────\n")
		fmt.Printf("Processed: %d  OK: %d  LLM-error: %d  Validation-warn: %d\n", n, nOK, nErr, nWarn)
		fmt.Printf("Total latency: %dms  Mean: %dms\n", totalMS, meanMS)
	}

	if *flagOut != "" {
		if err := writeMarkdownReport(*flagOut, records, *flagModel, *flagLanguage, meanMS); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write report to %s: %v\n", *flagOut, err)
		} else if !*flagJSON {
			fmt.Printf("Report written to %s\n", *flagOut)
		}
	}

	if *flagFailInvalid && nWarn > 0 {
		os.Exit(2)
	}
}

func processFile(path string, client *llm.Client) record {
	r := record{File: path}

	raw, err := os.ReadFile(path)
	if err != nil {
		r.ParseErr = err.Error()
		return r
	}

	var parsed model.Parsed
	if strings.EqualFold(filepath.Ext(path), ".txt") {
		parsed = model.Parsed{
			From:     model.Address{Addr: "(none)"},
			Subject:  "(none)",
			BodyText: strings.TrimSpace(string(raw)),
		}
	} else {
		var parseErr error
		parsed, parseErr = parse.Message(raw, *flagBodyLimit)
		if parseErr != nil {
			r.ParseErr = parseErr.Error()
			return r
		}
	}

	r.From = formatAddr(parsed.From)
	r.Subject = parsed.Subject
	r.HasAttach = parsed.HasAttachment
	r.Truncated = parsed.Truncated
	if *flagShowBody {
		r.Body = parsed.BodyText
	}

	in := llm.Input{
		From:     r.From,
		Subject:  parsed.Subject,
		Body:     parsed.BodyText,
		Language: *flagLanguage,
	}

	r.Summary, r.Debug = client.Summarize(context.Background(), in)
	r.Checks = checkSummary(r.Summary, parsed.Truncated)
	return r
}

func formatAddr(a model.Address) string {
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, a.Addr)
	}
	return a.Addr
}

// checkSummary runs semantic validation checks and returns any warnings.
func checkSummary(s model.Summary, truncated bool) []string {
	var warns []string

	if s.Status == "error" {
		warns = append(warns, "llm-fallback: "+s.Err)
		return warns
	}

	if len([]rune(s.Text)) > 200 {
		warns = append(warns, fmt.Sprintf("summary>200chars (%d)", len([]rune(s.Text))))
	}

	// Heuristic neutrality probe: flag obvious affect that should have been stripped.
	affectChecks := []struct {
		re   *regexp.Regexp
		name string
	}{
		{regexp.MustCompile(`!`), "affect-marker '!'"},
		{regexp.MustCompile(`[A-ZÀÂÆÇÉÈÊËÎÏÔŒÙÛÜ]{3,}`), "ALL-CAPS run"},
		{regexp.MustCompile(`(?i)\b(accuse|exige|insiste|blame)`), "accusation verb"},
	}
	for _, chk := range affectChecks {
		if chk.re.MatchString(s.Text) {
			warns = append(warns, chk.name)
		}
	}

	if truncated {
		warns = append(warns, "note: source was truncated")
	}
	return warns
}

func printHuman(r record, showBody bool) {
	const line = "────────────────────────────────────────────────────────────"
	label := r.File
	pad := ""
	if len(line)-len(label)-4 > 0 {
		pad = line[len(label)+4:]
	}
	fmt.Printf("\n── %s %s\n", label, pad)

	if r.ParseErr != "" {
		fmt.Printf("PARSE ERROR: %s\n", r.ParseErr)
		return
	}

	attach := ""
	if r.HasAttach {
		attach = "  [attachment]"
	}
	trunc := ""
	if r.Truncated {
		trunc = "  [truncated]"
	}
	fmt.Printf("From:    %s\n", r.From)
	fmt.Printf("Subject: %s%s%s\n", r.Subject, attach, trunc)
	fmt.Printf("Summary: %s\n", r.Summary.Text)

	dl := r.Summary.Deadline
	if dl == "" {
		dl = "(none)"
	}
	fmt.Printf("Action:  %-10s Deadline: %-12s Tone: %s\n", r.Summary.Action, dl, r.Summary.Tone)

	retried := "false"
	if r.Debug.Retried {
		retried = "true"
	}
	fmt.Printf("LLM:     %-6s (retried=%s, %dms)\n", r.Summary.Status, retried, r.Debug.LatencyMS)

	if len(r.Checks) == 0 {
		fmt.Printf("Checks:  OK\n")
	} else {
		fmt.Printf("Checks:  WARN  %s\n", strings.Join(r.Checks, "; "))
	}

	if showBody && r.Body != "" {
		fmt.Printf("--- cleaned body ---\n%s\n", r.Body)
	}
}

func printJSON(r record) {
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func writeMarkdownReport(path string, records []record, modelName, language string, meanMS int64) (retErr error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()

	ew := &errWriter{w: f}

	var nOK, nErr, nWarn int
	for _, r := range records {
		if r.ParseErr != "" || r.Summary.Status == "error" {
			nErr++
		} else {
			nOK++
		}
		if len(r.Checks) > 0 {
			nWarn++
		}
	}

	ew.printf("# Dev Summarize Report\n\n")
	ew.printf("**Model:** `%s` · **Language:** `%s` · **Generated:** %s\n\n",
		modelName, language, time.Now().UTC().Format(time.RFC3339))
	ew.printf("%d processed · %d OK · %d errors · %d warnings · mean %dms\n\n",
		len(records), nOK, nErr, nWarn, meanMS)

	for _, r := range records {
		subj := r.Subject
		if subj == "" {
			subj = filepath.Base(r.File)
		}
		ew.printf("---\n\n**%s**\n\n", subj)

		if r.ParseErr != "" {
			ew.printf("⚠️ Parse error: %s\n\n", r.ParseErr)
			continue
		}

		if r.Summary.Status == "error" {
			ew.printf("⚠️ could not summarize\n\n")
			continue
		}

		ew.printf("%s\n\n", r.Summary.Text)

		meta := fmt.Sprintf("`%s` · `%s`", r.Summary.Action, r.Summary.Tone)
		if r.Summary.Deadline != "" {
			meta += " · 📅 " + r.Summary.Deadline
		}
		meta += fmt.Sprintf(" · %dms", r.Debug.LatencyMS)
		if len(r.Checks) > 0 {
			meta += " · ⚠️ " + strings.Join(r.Checks, "; ")
		}
		ew.printf("%s\n\n", meta)
	}

	return ew.err
}

// errWriter accumulates write errors so callers avoid per-call error checks
// in sequential write sequences (devsummarize report only).
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, args ...any) {
	if ew.err == nil {
		_, ew.err = fmt.Fprintf(ew.w, format, args...)
	}
}


// resolveFiles returns all .eml and .txt files under root (or just root if it's a file).
func resolveFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("cannot access %q: %w", root, err)
	}
	if !info.IsDir() {
		ext := strings.ToLower(filepath.Ext(root))
		if ext != ".eml" && ext != ".txt" {
			return nil, fmt.Errorf("file must be .eml or .txt, got %q", filepath.Ext(root))
		}
		return []string{root}, nil
	}

	var files []string
	err = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".eml" || ext == ".txt" {
				files = append(files, path)
			}
		}
		return nil
	})
	return files, err
}

// preflight confirms Ollama is reachable and the model is pulled.
func preflight(ollamaURL, modelTag string) error {
	url := strings.TrimRight(ollamaURL, "/") + "/api/tags"
	resp, err := http.Get(url) //nolint:noctx // preflight is startup-only, not hot path
	if err != nil {
		return fmt.Errorf("cannot reach Ollama at %s: %w\nStart it with: ollama serve", ollamaURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck // error unrecoverable in defer

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama returned HTTP %d", resp.StatusCode)
	}

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return fmt.Errorf("decode /api/tags: %w", err)
	}

	want := normalizeModelTag(modelTag)
	for _, m := range tags.Models {
		if normalizeModelTag(m.Name) == want {
			return nil
		}
	}

	return fmt.Errorf("model %q not found in Ollama\nPull it with: ollama pull %s", modelTag, modelTag)
}

// normalizeModelTag strips the registry prefix for comparison (e.g. "library/gemma3:4b" → "gemma3:4b").
func normalizeModelTag(tag string) string {
	if idx := strings.LastIndex(tag, "/"); idx >= 0 {
		tag = tag[idx+1:]
	}
	return tag
}
