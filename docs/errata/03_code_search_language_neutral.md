# Erratum: code_search's examples and header languages

**Relates to:** spec 03 (`.specs/03_indexed_code_search`) — the `code_search`
tool description in `requirements.json`, and 03-REQ-5.2.
**Status:** raised in
[agent-fox-dev/agentkit-go#73](https://github.com/agent-fox-dev/agentkit-go/issues/73).

## What the spec said

The description carries four examples, one of them
`retry file:\.go$ -file:_test`. The index names a file's language from
outline's extension table.

## What is implemented

The example is `retry file:^src/ -file:test`. The description is part of the
model's prompt, and an example that names Go files reads as a hint that the
tool is for Go; a path filter shows the same `file:` and `-file:` syntax for
any language. The other three examples are unchanged.

A file's language comes from `outline.LangFor`, which reads the content as
well as the extension, so a `.h` header holding C++ is indexed as `C++` and
`lang:c++` finds it. SQL and R, which outline's table now lists, are no
longer in the index's own table of document languages; their names are
unchanged.
