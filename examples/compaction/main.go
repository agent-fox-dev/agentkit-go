// Command compaction shows a conversation outgrowing its context window and
// being summarized in place — with no API key and no network.
//
//	go run ./examples/compaction
//
// It drives the real loop and the real compaction transform against two
// scripted faux providers: one plays the conversation, the other plays the
// same model writing summaries. The model's context window is shrunk to 3,000
// tokens so the threshold is crossed in a handful of turns instead of a few
// hundred. Everything else — the anchored estimate, the cut, the split-turn
// summary, the failure taxonomy, the checkpoint that is re-applied on every
// later request, and the session log that carries it across a restart — is the
// shipped implementation.
//
// See examples/compaction/README.md for a walkthrough.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/compaction"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/provider/faux"
	"github.com/agentfox/agentkit-go/session"
	"github.com/agentfox/agentkit-go/stop"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// The questions a user asks over one long session. Each answer is padded to a
// realistic length, because the cut is chosen by size and a transcript of
// one-liners never has anything worth summarizing.
var questions = []string{
	"We are migrating the billing service from Postgres 12 to 16. What should we check first?",
	"The extension pg_partman is on 4.x. Does that block us?",
	"How do we rehearse the cutover without touching production?",
	"What is the rollback plan if replication lag spikes during cutover?",
	"Who needs to sign off before the maintenance window?",
	"Write the one-paragraph announcement for the #billing channel.",
	"Remind me: which extension did we say needed upgrading first?",
}

// usage is what a provider would report for one turn. The anchored estimate
// (compaction.EstimateContextTokens) starts from the newest reported usage and
// only estimates what came after it, so these numbers drive the trigger.
func usage(in, out int64) core.Usage {
	var u core.Usage
	u.SetField(core.UsageInputTokens, in)
	u.SetField(core.UsageOutputTokens, out)
	return u
}

func answer(n int, topic string) string {
	return fmt.Sprintf("Answer %d, on %s. ", n, topic) + strings.Repeat(
		"Here is the reasoning, the commands to run and the caveats worth writing down. ", 8)
}

func run(w io.Writer) error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "agentkit-compaction-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	logPath := filepath.Join(dir, "session.jsonl")

	// ---- The model. A real one comes from catalog.ResolveModel; here the
	// faux descriptor is shrunk so the threshold arrives quickly. The window
	// is the only number the Summarization strategy reads from the model.
	model := faux.Model()
	model.ContextWindow = 3000

	// ---- The conversation provider: one scripted turn per question, each
	// reporting the usage a real provider would. The sixth turn is the first
	// one sent over the COMPACTED view, so its reported input is small again.
	chatTurns := []faux.Turn{
		{Blocks: []core.ContentBlock{faux.FauxText(answer(1, "the upgrade checklist"))}, Usage: usage(400, 150)},
		{Blocks: []core.ContentBlock{faux.FauxText(answer(2, "pg_partman 4.x → 5.x"))}, Usage: usage(700, 150)},
		{Blocks: []core.ContentBlock{faux.FauxText(answer(3, "a rehearsal on a restored snapshot"))}, Usage: usage(1100, 150)},
		{Blocks: []core.ContentBlock{faux.FauxText(answer(4, "rollback by repointing PgBouncer"))}, Usage: usage(1450, 150)},
		{Blocks: []core.ContentBlock{faux.FauxText(answer(5, "sign-off from SRE and finance"))}, Usage: usage(1800, 150)},
		{Blocks: []core.ContentBlock{faux.FauxText(answer(6, "the announcement"))}, Usage: usage(600, 150)},
		{Blocks: []core.ContentBlock{faux.FauxText("pg_partman — it has to go from 4.x to 5.x before the major upgrade.")}, Usage: usage(750, 40)},
	}
	chat := faux.New(chatTurns...)

	// ---- The summarizer provider. In an application this is the SAME
	// registered provider as the conversation (see installCompaction in
	// examples/triage/triage.go); a second faux instance keeps the two
	// scripts readable. The first summary is truncated at max_tokens — which
	// the failure taxonomy rejects — and the next two succeed.
	summaries := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("Postgres 12→16 migration for billing. Decided: check extensions first; pg_par")},
			StopReason: core.StopReasonLength},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText(
			"Postgres 12→16 migration for billing. Decided: check extensions first; pg_partman must " +
				"go 4.x→5.x before the major upgrade; rehearse on a restored snapshot.")}},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText(
			"User asked for a rollback plan; assistant proposed repointing PgBouncer if lag spikes.")}},
	)

	// ---- The session log, so the checkpoint survives a restart.
	store, _, err := session.OpenOrCreate(logPath, session.Options{Durability: session.DurabilityPerEntry})
	if err != nil {
		return err
	}

	// ---- The wiring an application copies. Four things, in this order.
	//
	// 1. Make the history FIRST. The transform keeps its checkpoint on it,
	//    and the agent appends to it, so both must hold the same pointer.
	history := core.NewConversationHistory()

	cfg := core.AgentConfig{
		Model:        model,
		SystemPrompt: "You are a database migration assistant.",
		StopPolicy:   stop.AfterTurns(4),
		Providers:    core.ProviderRegistry{faux.API: chat.APIProvider()},
		SessionStore: store,
	}

	// 2. The transform. Summarizer and TurnSummarizer call the provider
	//    DIRECTLY — not through the agent — so a summary never passes through
	//    the middleware chain, the dedup cache or the budget gate.
	summarizer := core.ClientFunc(summaries.Stream)
	const reserveTokens = 1000 // the summary's max_tokens is 0.8 × this
	var lastSummary string
	cfg.TransformContext = compaction.NewContextTransform(compaction.Deps{
		Strategy:       compaction.Summarization{ThresholdFraction: 0.5, KeepTokens: 250},
		Summarizer:     compaction.ModelSummarizer(summarizer, model, reserveTokens),
		TurnSummarizer: compaction.ModelTurnSummarizer(summarizer, model, reserveTokens),
		History:        history,
		Model:          model,
		// 3. Persist the checkpoint as a compaction entry. The anchor is the
		//    ENTRY ID of the first kept message, not an index, because
		//    indices do not survive a re-parented log.
		OnCheckpoint: func(cp core.CompactionCheckpoint) error {
			firstKept := history.EntryIDAt(cp.PrefixLen)
			err := store.Append(session.NewCompactionEntry(cp.Summary, firstKept, lastSummary))
			lastSummary = cp.Summary
			fmt.Fprintf(w, "    [checkpoint] summarized messages 0..%d, kept from entry %s\n",
				cp.PrefixLen-1, firstKept)
			return err
		},
		// Compaction never aborts the session; without OnError a failed
		// summary is invisible.
		OnError: func(err error) { fmt.Fprintf(w, "    [compaction error] %v\n", err) },
	})

	// 4. Construct with the SAME history.
	agent, err := agentkit.NewAgentWithHistory(cfg, history)
	if err != nil {
		return err
	}

	section(w, "1. A conversation grows past the threshold")
	fmt.Fprintf(w, "context window %d tokens; Summarization fires above %d (50%%)\n\n",
		model.ContextWindow, model.ContextWindow/2)

	for i, q := range questions[:6] {
		if _, err := agent.Run(ctx, q); err != nil {
			return err
		}
		req := chat.Requests()[i]
		cp, hasCP := history.Checkpoint()
		est := compaction.EstimateContextTokens(req.Messages, checkpointPtr(cp, hasCP))
		fmt.Fprintf(w, "  turn %d: history %2d msgs → sent %2d msgs, est %4d tokens%s\n",
			i+1, history.Len(), len(req.Messages), est, summaryMark(req))
	}

	section(w, "2. What the summarizer was asked")
	for i, r := range summaries.Requests() {
		kind := "whole prefix"
		if strings.Contains(textOf(r.System), "PARTIAL turn") {
			kind = "split turn  "
		}
		fmt.Fprintf(w, "  call %d (%s): %d messages, max_tokens %d, tool_choice %q, session %s…\n",
			i+1, kind, len(r.Messages), *r.MaxTokens, r.ToolChoice, r.Options.SessionID[:12])
	}
	fmt.Fprintln(w, "\n  Call 1 was truncated at max_tokens, so it was REJECTED: no checkpoint, the")
	fmt.Fprintln(w, "  view went out unchanged, and the next turn tried again. A truncated summary")
	fmt.Fprintln(w, "  looks like a success and would poison every later turn.")

	section(w, "3. The checkpoint is permanent and re-applied")
	sent := chat.Requests()[5].Messages
	fmt.Fprintf(w, "  history still holds all %d messages (lossless log, scrollback works)\n", history.Len())
	fmt.Fprintf(w, "  the request sent %d: a summary, then the kept tail\n\n", len(sent))
	fmt.Fprintf(w, "  first sent message:\n%s\n", indent(firstText(sent), "    | "))
	_ = store.Close()

	section(w, "4. A restart folds the checkpoint back out of the log")
	store2, resume, err := session.OpenOrCreate(logPath, session.Options{Durability: session.DurabilityPerEntry})
	if err != nil {
		return err
	}
	defer store2.Close()
	fmt.Fprintf(w, "  folded %d messages; checkpoint present: %v (prefix %d)\n",
		len(resume.Messages), resume.HasCheckpoint, resume.Checkpoint.PrefixLen)

	// A resumed agent needs the transform re-bound to ITS history — the
	// one the fold built, which already carries the checkpoint.
	chat2 := faux.New(chatTurns[6])
	cfg2 := cfg
	cfg2.Providers = core.ProviderRegistry{faux.API: chat2.APIProvider()}
	cfg2.SessionStore = store2
	cfg2.TransformContext = compaction.NewContextTransform(compaction.Deps{
		Strategy:       compaction.Summarization{ThresholdFraction: 0.5, KeepTokens: 250},
		Summarizer:     compaction.ModelSummarizer(summarizer, model, reserveTokens),
		TurnSummarizer: compaction.ModelTurnSummarizer(summarizer, model, reserveTokens),
		History:        resume.History,
		Model:          model,
	})
	agent2, err := agentkit.NewAgentFromSession(cfg2, resume,
		func(string, core.API, string) (*core.Model, error) { return model, nil })
	if err != nil {
		return err
	}
	res, err := agent2.Run(ctx, questions[6])
	if err != nil {
		return err
	}
	req := chat2.Requests()[0]
	fmt.Fprintf(w, "  next turn sent %d of %d messages%s\n",
		len(req.Messages), agent2.History().Len(), summaryMark(req))
	fmt.Fprintf(w, "  answer: %q\n", res.FinalText())
	fmt.Fprintln(w, "\n  The new process paid for no summary: the one written before the restart")
	fmt.Fprintln(w, "  was folded from the log and applied before the first request.")
	return nil
}

// summaryMark labels a request whose first message is the compaction summary.
func summaryMark(req core.Request) string {
	if strings.HasPrefix(firstText(req.Messages), compaction.SummaryPrefix) {
		return "  ← starts with the summary"
	}
	return ""
}

func firstText(msgs core.Messages) string {
	if len(msgs) == 0 {
		return ""
	}
	if u, ok := msgs[0].(core.UserMessage); ok {
		return u.Content.Text()
	}
	return ""
}

func textOf(blocks []core.ContentBlock) string {
	return core.Content(blocks).Text()
}

func checkpointPtr(cp core.CompactionCheckpoint, ok bool) *core.CompactionCheckpoint {
	if !ok {
		return nil
	}
	return &cp
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n── %s %s\n", title, strings.Repeat("─", max(0, 66-len(title))))
}

func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}
