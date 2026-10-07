# Erratum: the build deadline keeps the walked files for a grace window

**Relates to:** spec 03 (`.specs/03_indexed_code_search`) — PRD §4 and
03-REQ-5.7 ("stop the build, keep what it has indexed").
**Status:** implemented 2026-10-07, from
[agent-fox-dev/agentkit-go#90](https://github.com/agent-fox-dev/agentkit-go/issues/90).

## What the code did

When `MaxBuildTime` fired, every stage stopped: the outline loop cut the file
list to the files already outlined, and the builder added only those. A
deadline that fired DURING THE WALK left nothing outlined, so the build kept
nothing — `partial: true, reason: time, files_indexed: 0` — and because a
partial index is not retried (Design Decision 10), a large monorepo or a slow
filesystem had a permanently empty index.

## What it does now

The files the walk found are kept. Those not yet outlined are indexed without
symbols, and the builder may add them for a GRACE WINDOW of a quarter of
`MaxBuildTime` after the deadline, then stops. A build therefore runs for at
most 1.25 × `MaxBuildTime`, and keeps what it has rather than nothing.

Adding every walked file without a limit was rejected: on the slow filesystem
that made the walk hit the deadline, reading the files it found could take as
long again, and the bound would no longer be one. The earlier test that pinned
"no file is added after the deadline" (`TestDeadlineHitInTheWalkStopsTheBuilderLoop`)
is now `TestDeadlineHitInTheWalkKeepsWhatItFoundWithinAGraceWindow`: files are
kept within the window, and none are added when the window is empty.
