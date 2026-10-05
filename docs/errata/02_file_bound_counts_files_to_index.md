# Erratum: the symbol table's file bound counts the files a pass indexes

**Relates to:** spec 02 (`.specs/02_symbol_navigation_tools`) — PRD §4 and
Design Decision 7; 02-REQ-5.7; TS-02-37.
**Status:** raised in
[agent-fox-dev/agentkit-go#57](https://github.com/agent-fox-dev/agentkit-go/issues/57).

## What the spec said

PRD §4: "The file bound counts regular files the walk yields, whatever their
language; the pass stops at the first file past the limit."

Design Decision 7: "**The file bound counts every regular file the walk
yields.** [...] Counting files in unknown languages makes the bound a bound on
walk work, which is what takes time; a table that counted only source files
would let a tree of a million PDFs walk unbounded."

## What was delivered

`buildOrRefresh` (`tools/symbols.go`) counts only the regular files a pass has
to index: files that are new, changed, or racy (02-REQ-6.6). A file that is
already in the table with an unchanged size and mtime is skipped before it is
counted. The comment at that line says so.

## Why both cannot hold

02-REQ-5.7 and PRD §4 also say that a partial table is not final: "the next call
runs another pass that skips files whose size and mtime are unchanged and
indexes the ones not yet seen, so repeated calls make progress", until the table
is complete. The walk visits files in a fixed order (`filepath.WalkDir`, which is
lexical). If the files a pass skips counted toward the bound, the second pass
over a tree of 2N files with a bound of N would yield the N files it already
indexed first, reach the bound at the next file, and stop. It would never index
anything past the first N, so a tree larger than the bound could never become
complete. `TestRepeatedCallsAfterPartial_TS02_37` (a 40-file tree) pins the
behaviour the requirement asks for.

## What is implemented

The bound counts the regular files a pass indexes, whatever their language: a
file in a language `outline` does not know is still counted, so DD 7's concern
about a tree of a million PDFs holds for the pass that indexes them, which stops
at the bound. Files skipped as unchanged are not counted.

The walk's own work, which a later pass spends stat-ing the files it already
indexed, is bounded by the other bound: `SymbolOptions.MaxDuration` covers the
walk and the outlining together, and a pass that reaches it stops and is partial.

The spec file is not edited; `.specs/` records what was asked for, and this
erratum records how the two statements were reconciled.
