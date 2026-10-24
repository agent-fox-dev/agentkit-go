# Erratum: spec 03 `indexed_code_search`

These are the places the delivered code and its tests differ from the spec's
text, and why.

## The go/no-go figures and zoekt answers live in `docs/codesearch.md` (03-REQ-1.2, 03-REQ-9.1, 03-REQ-10.5)

03-REQ-1.2 and 03-REQ-9.1 have `codesearch/README.md` lead with the go/no-go
gate and record the answers about the pinned zoekt release (module path,
packages, symbol sections, search options, cross-target results), and TS-03-6
checked that the README's first section was the gate.

The repository's documentation rules (`.specs/steering.md`, Documentation
Style) now say a README introduces a module to its users — what it is, the
problem it solves, a short example, where to read more — and that
implementation records belong in `docs/`. The README was rewritten that way,
and the gate, the measured figures, the differences from `search_files` and
the zoekt answers moved unchanged to `docs/codesearch.md`, which the README
links to.

TS-03-6 (`TestReadmeContent_TS03_6`, `codesearch/docs_test.go`) now reads
`docs/codesearch.md`. It finds the gate by a `##` heading naming the
Go/No-Go gate rather than by position, since the document opens with a
contents index, and checks the same decision, date, repositories and topics as
before.
