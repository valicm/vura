// Package describe asks the local `claude` CLI for worklog prose. Language
// only: it never sees or produces hours, buckets or tickets to log against.
// Absent or failing CLI is not an error for callers; they keep the
// deterministic text.
package describe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Input is everything the model may use.
type Input struct {
	Client   string   // bucket label
	Tickets  []string // keys seen in the session
	Commits  []string // commit subjects
	Evidence string   // "editor ×41 · 3 commits" style summary
	Current  string   // the deterministic description, as a starting point
}

// Available reports whether the claude CLI is on PATH.
func Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// Worklog returns a one-line description under 200 characters.
func Worklog(ctx context.Context, in Input) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var b strings.Builder
	b.WriteString("Write one Tempo worklog description for a freelance developer's client invoice.\n")
	b.WriteString("Rules: one line, under 200 characters, lead with the ticket key(s) if any, plain factual past tense, no preamble, no quotes, no trailing period.\n")
	b.WriteString("Do not invent work that is not in the evidence. If the evidence is only 'development', say what area the repo name suggests and keep it generic.\n\n")
	fmt.Fprintf(&b, "Client: %s\n", in.Client)
	if len(in.Tickets) > 0 {
		fmt.Fprintf(&b, "Tickets: %s\n", strings.Join(in.Tickets, ", "))
	}
	if len(in.Commits) > 0 {
		b.WriteString("Commits:\n")
		for _, c := range in.Commits {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if in.Evidence != "" {
		fmt.Fprintf(&b, "Evidence: %s\n", in.Evidence)
	}
	if in.Current != "" {
		fmt.Fprintf(&b, "Current draft: %s\n", in.Current)
	}
	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "json", b.String())
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("claude: %v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("claude: %w", err)
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("claude: decode: %w", err)
	}
	text := strings.TrimSpace(strings.Trim(strings.TrimSpace(resp.Result), `"“”`))
	text = strings.TrimSuffix(text, ".")
	if text == "" {
		return "", fmt.Errorf("claude: empty result")
	}
	if len(text) > 250 {
		text = text[:247] + "..."
	}
	return text, nil
}
