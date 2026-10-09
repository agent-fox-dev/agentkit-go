package core

// Resolve is REQ-TOOL-10's five-field resolution over a registered set. It applies
// UNIFORMLY to built-in and caller-supplied tools — that uniformity is the
// requirement, and it is what makes four otherwise surprising consequences
// true. Each is normative and each has a test:
//
//	NoTools "all"     disables CUSTOM tools too, not just built-ins.
//	NoTools "builtin" leaves the allowlist unset, so custom tools SURVIVE.
//	ToolNames         constrains custom tools, not only built-ins.
//	ExcludeTools      applies to custom tools, not only built-ins.
//
// Resolution order is Tools → NoTools → ToolNames → ExcludeTools, with
// CustomTools merged in before name-based selection so the selectors see one
// undifferentiated set.
//
// This is what REQ-MULTI-05's per-agent "tool allowlist" resolves to, and it
// is what lets an agent be scoped to read-and-search-only without rebuilding
// the tool set by hand.
func (p ToolPolicy) Resolve(registered []Tool) []Tool {
	// Tools, when non-nil INCLUDING EMPTY, is used verbatim and bypasses
	// everything below. Non-nil-but-empty is deliberately distinct from nil:
	// "no tools, and I mean it" must be expressible.
	if p.Tools != nil {
		return append([]Tool(nil), p.Tools...)
	}

	set := make([]Tool, 0, len(registered)+len(p.CustomTools))
	seen := make(map[string]int, len(registered)+len(p.CustomTools))

	add := func(t Tool) {
		if i, ok := seen[t.Name]; ok {
			// A custom tool overrides a built-in of the same name, in place,
			// so overriding does not reorder the tool list — the tool list is
			// part of the cached prompt prefix.
			set[i] = t
			return
		}
		seen[t.Name] = len(set)
		set = append(set, t)
	}

	if p.NoTools != NoToolsAll {
		for _, t := range registered {
			if t.Builtin && p.NoTools == NoToolsBuiltin {
				continue
			}
			add(t)
		}
		for _, t := range p.CustomTools {
			add(t)
		}
	}

	// NoTools "all" sets an empty allowlist: nothing survives, custom
	// included. Returning here rather than falling through keeps that true
	// even if ToolNames names something.
	if p.NoTools == NoToolsAll {
		return nil
	}

	// ToolNames nil means "the default set"; non-nil is an allowlist applied
	// to everything, custom tools included.
	if p.ToolNames != nil {
		allow := make(map[string]bool, len(p.ToolNames))
		for _, n := range p.ToolNames {
			allow[n] = true
		}
		set = filterTools(set, func(t Tool) bool { return allow[t.Name] })
	}

	// ExcludeTools is a denylist applied AFTER the allowlist, and applies to
	// custom tools too.
	var deny map[string]bool
	if len(p.ExcludeTools) > 0 {
		deny = make(map[string]bool, len(p.ExcludeTools))
		for _, n := range p.ExcludeTools {
			deny[n] = true
		}
		set = filterTools(set, func(t Tool) bool { return !deny[t.Name] })
	}

	// The same selection applies to what each surviving tool reaches
	// (07-REQ-2.3): a policy that excludes a tool excludes it behind a
	// wrapper too, or the wrapper would be a way around the policy.
	var allow map[string]bool
	if p.ToolNames != nil {
		allow = make(map[string]bool, len(p.ToolNames))
		for _, n := range p.ToolNames {
			allow[n] = true
		}
	}
	admit := func(t Tool) bool {
		if t.Builtin && p.NoTools == NoToolsBuiltin {
			return false
		}
		return (allow == nil || allow[t.Name]) && !deny[t.Name]
	}
	return pruneReachable(set, admit)
}

// pruneReachable filters each tool's ReachableTools by admit, recursively,
// into fresh slices so the registered tools are never modified. A wrapper
// that reached something and is left reaching nothing is dropped
// (07-REQ-2.4): it would be a dead tool in the model's prompt. A tool that
// never reached anything is kept (07-REQ-2.5).
func pruneReachable(ts []Tool, admit func(Tool) bool) []Tool {
	out := ts[:0]
	for _, t := range ts {
		if len(t.ReachableTools) > 0 {
			var kept []Tool
			for _, r := range t.ReachableTools {
				if admit(r) {
					kept = append(kept, r)
				}
			}
			kept = pruneReachable(kept, admit)
			if len(kept) == 0 {
				continue
			}
			t.ReachableTools = kept
		}
		out = append(out, t)
	}
	return out
}

func filterTools(ts []Tool, keep func(Tool) bool) []Tool {
	out := ts[:0]
	for _, t := range ts {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}
