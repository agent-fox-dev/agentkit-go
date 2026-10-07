# Erratum: a custom system prompt keeps the tools' guidelines

**Relates to:** NFR-TEST-08(a), NFR-TEST-08a, REQ-TOOL-04e.
**Status:** raised in
[agent-fox-dev/agentkit-go#75](https://github.com/agent-fox-dev/agentkit-go/issues/75).

## What the PRD says

NFR-TEST-08(a) asks for a golden "pinning the custom-system-prompt branch
(assembly order and the assertion that built-in blocks are absent)".
NFR-TEST-08a: "Per-tool usage guidance lives on the tool as
`Tool.PromptGuidelines`, not in a separate prompt file that drifts from the
tool. The prompt builder collects guidelines across the resolved tool set
[…] and appends the universal guidelines last."

## What the code did

`prompt.Build` read "built-in blocks" as everything but the custom text and the
skills/context blocks: with `AgentConfig.SystemPrompt` set, no guideline
reached the model — not the tools' `PromptGuidelines`, not REQ-TOOL-04e's
execute fallback, not the search-over-execute line. Every embedder with its own
prompt (agent-fox among them) had to re-render the guidelines by hand. The
shell guidelines were also keyed on the literal tool name `execute`, so a set
with `run_command` or `powershell` and no `execute` got none.

## What is implemented

"Built-in" means the text AgentKit itself authors for the default persona:
`BaseInstructions` and `UniversalGuidelines`. Those a custom prompt replaces.
The tools' guidelines are part of the tools (NFR-TEST-08a) and follow a custom
prompt, in a `Guidelines:` block after it and before the skills and context
blocks. The shell guidelines key on whichever of `execute`, `run_command` and
`powershell` is active (`guard.ShellToolNames` order) and name it; for
`execute` their text is unchanged, byte for byte.

`TestGoldenCustomSystemPrompt` asserts the base instructions and the universal
guidelines are absent and a tool guideline present; its golden was updated and
the diff reviewed — the added lines are exactly the default tool set's own
guidelines.
