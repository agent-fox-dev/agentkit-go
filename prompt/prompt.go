// Package prompt assembles the system prompt the loop sends (NFR-TEST-08a,
// REQ-TOOL-04e).
//
// Until this existed, core.Tool.PromptGuidelines was a field nothing read and
// the loop sent AgentConfig.SystemPrompt verbatim — so a tool could declare
// guidance the model never saw. NFR-TEST-08(a) asks for a golden of the
// assembled prompt "built through the real tool resolver", which needs a real
// assembler to build it. Build is that assembler.
package prompt

import (
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// Section names a block of the assembled prompt. The order of this
// declaration is the order they appear, and the golden pins it.
type Section string

const (
	SectionBase       Section = "base"
	SectionTools      Section = "tools"
	SectionGuidelines Section = "guidelines"
)

// BaseInstructions is the built-in opening section.
//
// Deliberately short. Everything specific to what the agent can actually do
// comes from the resolved tool set, which is the part that changes; a long
// fixed preamble is tokens paid on every request to say things the tool
// descriptions already say.
const BaseInstructions = `You are an AI assistant with access to tools.

Work from evidence: read before you edit, and verify a change rather than
assuming it worked. When a tool reports an error, read the error — it names
what to do differently. Prefer one correct call to several speculative ones.`

// UniversalGuidelines are appended after the per-tool ones (NFR-TEST-08a:
// "appends the universal guidelines last").
//
// Last because they are the weakest: a per-tool guideline is specific advice
// about a tool the model is looking at, and burying it under general
// exhortations is how it stops being read.
var UniversalGuidelines = []string{
	"Do not guess at file contents or APIs; read them.",
	"Report what you actually did, including what failed.",
}

// Input is everything the assembler needs.
type Input struct {
	// Custom replaces the BUILT-IN sections when non-empty
	// (AgentConfig.SystemPrompt): the base instructions and the universal
	// guidelines. The active tools' own guidelines still follow it.
	Custom string
	// Tools is the set actually active after the REQ-TOOL-10 policy resolved,
	// never the registry. The guidelines the model sees must describe the
	// tools it has.
	Tools []core.Tool
	// ExtraBlocks are appended after the built-in sections, in order: project
	// context, or anything else an embedder assembles itself.
	ExtraBlocks []string
}

// Build assembles the prompt.
//
// A CUSTOM prompt replaces the built-in base and the built-in UNIVERSAL
// guidelines, and nothing else. The tools' guidelines are not built-in text:
// they travel with the tool (NFR-TEST-08a) and describe how to use what the
// model has been given, so they follow a custom prompt as they follow the
// built-in one — otherwise every embedder with its own prompt has to re-render
// them by hand. Extra blocks still append too: they are the embedder's own
// text, and a custom prompt silently dropping them would be a surprise.
func Build(in Input) string {
	var blocks []string

	if in.Custom != "" {
		blocks = append(blocks, strings.TrimRight(in.Custom, "\n"))
		if g := guidelinesBlock(in.Tools, false); g != "" {
			blocks = append(blocks, g)
		}
	} else {
		blocks = append(blocks, BaseInstructions)
		if g := guidelinesBlock(in.Tools, true); g != "" {
			blocks = append(blocks, g)
		}
	}

	for _, b := range in.ExtraBlocks {
		if b = strings.TrimRight(b, "\n"); b != "" {
			blocks = append(blocks, b)
		}
	}
	return strings.Join(blocks, "\n\n")
}

// guidelinesBlock is NFR-TEST-08a's collection. universal adds the built-in
// UniversalGuidelines, which a custom prompt replaces.
//
// Deduplicated while PRESERVING FIRST-SEEN ORDER. Sorting would be tidier and
// wrong: the order tools were resolved in is the order the model reads them,
// and an alphabetical list separates a guideline from the tool it is about.
func guidelinesBlock(active []core.Tool, universal bool) string {
	var (
		lines []string
		seen  = map[string]bool{}
	)
	add := func(g string) {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			return
		}
		seen[g] = true
		lines = append(lines, "- "+g)
	}

	for _, t := range active {
		for _, g := range t.PromptGuidelines {
			add(g)
		}
	}
	// REQ-TOOL-04e: emitted when the file-navigation tools are ABSENT and a
	// shell is present. It cannot be a PromptGuidelines entry on any tool,
	// because a per-tool field can only fire when its tool is there — which is
	// the opposite of the condition. The shell is whichever of execute and
	// run_command is active, and the guideline names it: a
	// guideline about `execute` given to a model that has `run_command` points
	// at a tool it cannot call.
	if shell := activeShell(active); shell != "" {
		if !anyPresent(active, tools.FileNavigationTools()) {
			add(shellGuideline(tools.ExecuteFallbackGuideline, shell))
		}
		if hasTool(active, "search_files") {
			add(shellGuideline(tools.SearchOverExecuteGuideline, shell))
		}
	}
	if universal {
		for _, g := range UniversalGuidelines {
			add(g)
		}
	}

	if len(lines) == 0 {
		return ""
	}
	return "Guidelines:\n" + strings.Join(lines, "\n")
}

// activeShell returns the first shell tool in the active set, in
// guard.ShellToolNames order, or "".
func activeShell(active []core.Tool) string {
	for _, n := range guard.ShellToolNames {
		if hasTool(active, n) {
			return n
		}
	}
	return ""
}

// shellGuideline is a guideline written about execute, naming shell instead.
// For execute itself it is the pinned text, byte for byte.
func shellGuideline(g, shell string) string {
	return strings.ReplaceAll(g, "execute", shell)
}

func hasTool(set []core.Tool, name string) bool {
	for _, t := range set {
		if t.Name == name {
			return true
		}
	}
	return false
}

func anyPresent(set []core.Tool, names []string) bool {
	for _, n := range names {
		if hasTool(set, n) {
			return true
		}
	}
	return false
}
