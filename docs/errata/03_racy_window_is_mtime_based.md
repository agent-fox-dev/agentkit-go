# Erratum: the racy window is about a file's mtime, not about when it was indexed

**Relates to:** spec 03 (`.specs/03_indexed_code_search`) — 03-REQ-6.2 and PRD §5;
spec 02 (`.specs/02_symbol_navigation_tools`) — 02-REQ-6.6 and Design Decision 12.
**Status:** raised in
[agent-fox-dev/agentkit-go#56](https://github.com/agent-fox-dev/agentkit-go/issues/56)
(and earlier in #54), which asked for the two specs to be reconciled.

## What the specs said

Spec 02 (PRD §5, 02-REQ-6.6, Design Decision 12): "A file **whose mtime lies
within 2 s of the moment it was indexed** is always re-outlined on
revalidation, so an edit that keeps the size and falls inside the filesystem's
timestamp granularity is not missed."

Spec 03 (PRD §5, 03-REQ-6.2) words the same window as "treating a file
**indexed within the previous 2 seconds** as changed (the filesystem
timestamp-granularity window `02_symbol_navigation_tools` uses)".

## Why they cannot both hold

The two sentences describe different rules, and only the first closes the hole
Design Decision 12 describes. A same-size edit made in the same timestamp
granule as the write that was indexed leaves `(size, mtime)` unchanged. A rule
about *when the file was indexed* only sees that edit if the revalidation
follows the build within 2 s; a revalidation 5 s later skips the file and serves
stale content. A rule about the file's *mtime* sees it at any time.

The "indexed within 2 s" reading has a second cost: every file indexed in the
last 2 s counts as changed, which for a tree indexed just before a shell command
marks the whole index dirty and, in `codesearch`, forces a full rebuild.

## What is implemented

Both implementations follow spec 02's rule. Spec 03's parenthetical, which names
spec 02's window as its source, is read as meaning that rule, and "a file indexed
within the previous 2 seconds" as shorthand for "a file whose mtime lies within 2
seconds of the moment it was indexed".

| | Symbol table (`tools/symbols.go`) | `codesearch` index (`codesearch/dirty.go`) |
|---|---|---|
| A file is racy when | `\|indexedAt − mtime\| ≤ 2 s` | `indexedAt − mtime ≤ 2 s` (a recorded mtime later than the index time is racy too) |
| A racy file with unchanged `(size, mtime)` is | re-outlined | compared with a content hash recorded at build time, and dirty only if the content differs |
| It stops being racy | when a re-outline records `indexedAt` more than 2 s after its mtime | when a comparison made after the file's granule closed has found it unchanged |

The differences are deliberate. The symbol table re-outlines because outlining
is the work it does and spec 02 says "re-outlined"; the index compares content
because re-indexing a file means marking it dirty, which counts toward the 5%
rebuild threshold. A recorded mtime far in the future of the index time can only
come from clock skew; the symbol table treats it as not racy, `codesearch` as
racy, which costs it only a content comparison.

Neither spec file is edited; `.specs/` records what was asked for, and this
erratum records how the two statements were read.
