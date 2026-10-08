// Command branching shows the session log as a tree: rewinding a
// conversation to an earlier point, carrying a summary of the abandoned path
// into the new one, and later resuming either branch — all in one
// append-only file.
//
//	go run ./examples/branching
//
// It runs with no API key and no network: the real loop and the real session
// store run against a scripted faux provider, and the program prints what the
// model was actually sent at each step, so you can see which branch it saw.
//
// See examples/branching/README.md for a walkthrough.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentkit "github.com/agentfox/agentkit-go"
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

func run(w io.Writer) error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "agentkit-branching-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "session.jsonl")

	// Readable entry ids (e01, e02, …) so the tree below is easy to follow.
	// The default is 16 random bytes; NewID exists so tests can pin a file.
	// The session header takes the first one, so entries start at e02.
	n := 0
	opts := session.Options{NewID: func() core.EntryID { n++; return core.EntryID(fmt.Sprintf("e%02d", n)) }}

	// ------------------------------------------------------------------ 1
	section(w, "1. a conversation, recorded")
	store, resume, err := session.OpenOrCreate(path, opts)
	if err != nil {
		return err
	}
	p := faux.New(
		reply("Postgres, with the events table partitioned by month."),
		reply("CREATE TABLE events (id bigint, at timestamptz, body jsonb) PARTITION BY RANGE (at);"),
		reply("Use pg_partman to create partitions and drop ones older than 90 days."),
	)
	agent, err := agentkit.NewAgentFromSession(config(p, store), resume, sameModel)
	if err != nil {
		return err
	}
	for _, q := range []string{
		"Which database for an append-only event log?",
		"Show me the schema.",
		"How do we expire old events?",
	} {
		if _, err := agent.Run(ctx, q); err != nil {
			return err
		}
	}
	printTree(w, store.Entries(), store.Head())
	firstLeaf := store.Head()

	// ------------------------------------------------------------------ 2
	section(w, "2. rewind to the first answer and take another path")
	// The fork point is the entry the new branch hangs off: here, the first
	// assistant answer. Find it however your UI lets the user pick it.
	forkPoint := nthMessage(store.Entries(), core.RoleAssistant, 0)

	// ForkFrom moves the head; nothing is rewritten. The branch summary is
	// the first entry on the new branch, so the model is told what was
	// tried — without the abandoned turns themselves.
	if err := store.ForkFrom(forkPoint); err != nil {
		return err
	}
	summary := "We chose Postgres, drafted a partitioned events table, and planned pg_partman for 90-day retention."
	if err := store.Append(session.NewBranchSummaryEntry(summary, firstLeaf, forkPoint)); err != nil {
		return err
	}

	// FoldLeaf folds the new branch into construction inputs. An agent is
	// built from a Resume, never re-pointed: the one built in section 1 is
	// still on the old branch, and must not be used again.
	resume, err = session.FoldLeaf(store, store.Head())
	if err != nil {
		return err
	}
	p = faux.New(reply("ClickHouse: a MergeTree table ordered by time, with a TTL clause for expiry."))
	agent, err = agentkit.NewAgentFromSession(config(p, store), resume, sameModel)
	if err != nil {
		return err
	}
	if _, err := agent.Run(ctx, "Actually, what would this look like in ClickHouse?"); err != nil {
		return err
	}
	fmt.Fprintln(w, "  what the model was sent on the new branch:")
	printRequest(w, p.Requests()[0])
	fmt.Fprintln(w)
	printTree(w, store.Entries(), store.Head())
	if err := store.Close(); err != nil {
		return err
	}

	// ------------------------------------------------------------------ 3
	section(w, "3. both branches are in one file")
	loaded, err := session.Load(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  session.Load: %d entries, leaves %v, head %s (the last entry written)\n",
		len(loaded.Entries()), loaded.Leaves(), loaded.Head())
	for _, leaf := range loaded.Leaves() {
		branch, err := loaded.Branch(leaf)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "  branch to %s: %s\n", leaf, ids(branch))
	}

	// ------------------------------------------------------------------ 4
	section(w, "4. a later process resumes the ORIGINAL branch")
	store2, onHead, err := session.OpenOrCreate(path, opts)
	if err != nil {
		return err
	}
	defer store2.Close()
	fmt.Fprintf(w, "  OpenOrCreate folded the head branch, ending at %s\n", onHead.LeafID)

	// The guard: an agent built from a Resume that is not the branch the
	// store will append to is refused, because the model would be sent one
	// conversation while the log recorded another.
	if err := store2.ForkFrom(firstLeaf); err != nil {
		return err
	}
	if _, err := agentkit.NewAgentFromSession(config(faux.New(), store2), onHead, sameModel); err != nil {
		fmt.Fprintf(w, "  refused, as it should be: %s\n", firstLine(err.Error()))
	}

	// The right way: FoldLeaf moves the head AND folds that branch.
	back, err := session.FoldLeaf(store2, firstLeaf)
	if err != nil {
		return err
	}
	p = faux.New(reply("Run pg_partman's maintenance from pg_cron every night."))
	agent, err = agentkit.NewAgentFromSession(config(p, store2), back, sameModel)
	if err != nil {
		return err
	}
	if _, err := agent.Run(ctx, "How do we schedule that?"); err != nil {
		return err
	}
	fmt.Fprintln(w, "  what the model was sent back on the original branch:")
	printRequest(w, p.Requests()[0])
	fmt.Fprintln(w)
	printTree(w, store2.Entries(), store2.Head())
	return nil
}

// ---------------------------------------------------------------------------

func config(p *faux.Provider, store core.SessionStore) core.AgentConfig {
	return core.AgentConfig{
		Model:        faux.Model(),
		SystemPrompt: "You are a database design assistant.",
		StopPolicy:   stop.AfterTurns(4),
		Providers:    core.ProviderRegistry{faux.API: p.APIProvider()},
		SessionStore: store,
		// Silent persistence failure is not allowed: a store driven by the
		// loop reports here.
		OnPersistError: func(err error) { fmt.Fprintln(os.Stderr, "session:", err) },
	}
}

// sameModel resolves the model a session recorded. A real application looks
// the triple up in its catalog (catalog.ResolveModel).
func sameModel(string, core.API, string) (*core.Model, error) { return faux.Model(), nil }

func reply(text string) faux.Turn {
	return faux.Turn{Blocks: []core.ContentBlock{faux.FauxText(text)}, StopReason: core.StopReasonStop}
}

// nthMessage finds the id of the i-th message entry with the given role.
func nthMessage(entries []core.Entry, role core.Role, i int) core.EntryID {
	for _, e := range entries {
		if e.Type == core.EntryMessage && e.Message != nil && e.Message.Message.Role() == role {
			if i == 0 {
				return e.ID
			}
			i--
		}
	}
	return core.NullLeaf
}

// printTree renders the log as the tree its parent links make, marking the
// head. Every entry stays in the file forever; a branch is a root→leaf path.
func printTree(w io.Writer, entries []core.Entry, head core.EntryID) {
	kids := map[core.EntryID][]core.Entry{}
	for _, e := range entries {
		kids[e.ParentID] = append(kids[e.ParentID], e)
	}
	var walk func(parent core.EntryID, depth int)
	walk = func(parent core.EntryID, depth int) {
		for i, e := range kids[parent] {
			pad := strings.Repeat("   ", depth)
			mark := ""
			if e.ID == head {
				mark = "   ← head"
			}
			branch := "├─"
			if len(kids[parent]) == 1 || i == len(kids[parent])-1 {
				branch = "└─"
			}
			if len(kids[parent]) > 1 {
				fmt.Fprintf(w, "  %s%s %s %s%s\n", pad, branch, e.ID, label(e), mark)
				walk(e.ID, depth+1)
				continue
			}
			fmt.Fprintf(w, "  %s%s %s%s\n", pad, e.ID, label(e), mark)
			walk(e.ID, depth)
		}
	}
	walk(core.NullLeaf, 0)
}

func label(e core.Entry) string {
	switch e.Type {
	case core.EntryMessage:
		m := e.Message.Message
		text := ""
		switch v := m.(type) {
		case core.UserMessage:
			text = v.Content.Text()
		case core.AssistantMessage:
			text = v.Content.Text()
		}
		return fmt.Sprintf("%-9s %s", m.Role(), clip(text, 54))
	case core.EntryBranchSummary:
		return fmt.Sprintf("%-9s %s", "summary", clip(e.BranchSummary.Summary, 54))
	}
	return string(e.Type)
}

func printRequest(w io.Writer, req core.Request) {
	for _, m := range req.Messages {
		text := ""
		switch v := m.(type) {
		case core.UserMessage:
			text = v.Content.Text()
		case core.AssistantMessage:
			text = v.Content.Text()
		}
		fmt.Fprintf(w, "    %-9s %s\n", m.Role(), clip(strings.ReplaceAll(text, "\n", " "), 80))
	}
}

func ids(es []core.Entry) string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = string(e.ID)
	}
	return strings.Join(out, " → ")
}

func clip(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func firstLine(s string) string {
	if i := strings.Index(s, "; "); i >= 0 {
		return s[:i]
	}
	return s
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n── %s %s\n", title, strings.Repeat("─", max(0, 70-len(title))))
}
