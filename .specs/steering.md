# Steering Directives

This file is kept the same across the agent-fox repositories. Anything that is
specific to one repository lives in the **Repository Specifics** section at the
end.

## Library Reuse

1. Before implementing any functionality, check whether it already exists in the codebase.
2. **go.mod dependencies** -- check existing imports before adding new ones.
3. **Standard library** -- prefer `net/http`, `slices`, `encoding/json`, etc. over third-party alternatives.

## Target Platforms

The software MUST run on Linux or macOS systems.
Support for Windows or other operating systems is "best effort", but not a requirement.

## Quality Gate

While implementing anything — a spec, a fix, a refactor — the **only** quality
gate you run is `make check`. It is the fast check: `fmt`, `vet`, `lint` and the
short tests.

1. Do NOT run `make check-release`. It is a manual action, run by the user
   before a release or a merge; it is never part of an agent session.
2. Do not run the full `make test` as a substitute for it. Narrow `go test`
   runs on the packages you touch are fine while you work.
3. A spec's `tasks.json` test command, a skill or a checklist that names
   `make check-release` or the full `make test` does not override this rule:
   run `make check` instead and say in the handoff note that the full gate was
   left to the user.
4. If you believe a change needs the full gate (it touches the slow gates in
   `internal/policy`, the cross-target builds or the nested `make` runs), say so
   in the handoff note. Do not run it yourself.

## Documentation Freshness

After implementing any spec, you **must** update all affected documentation
before the session is considered complete. Outdated docs are treated as
regressions.

Before updating the documentation, run `make doc` first, to refresh the generated Go docs (see below).

**When to update:** Every time a spec implementation adds, changes, or removes
any of the items listed below, update the corresponding document in the same
session — not as a follow-up task.

| What changed | Update |
|---|---|
| REST endpoints or the public Go API added/changed/removed | `docs/api.md` |
| CLI commands, subcommands, or flags added/changed/removed | `docs/cli.md` |
| Config keys or env vars added/changed | `docs/configuration.md` |
| Architecture, package layout, or data flow changed | `docs/architecture.md` and/or relevant ADR |
| Setup, quickstart, or project overview changed | `README.md` |

*Important:* Do NOT write tests for testing the presence or the content of the docs or PRDs.

**Instructions:**

1. Review the spec you just implemented and identify every user-facing or
   developer-facing surface that changed (API routes, request/response
   schemas, CLI parameters, environment variables, architectural decisions).
2. Open each affected doc and update it to reflect the new state. Do not
   leave placeholder text like "TODO" or "TBD" — write the actual content.
3. If a doc file listed above does not exist yet, create it with the correct
   content rather than skipping the update. A repository without a REST API or
   CLI keeps the file anyway and says in one line that it has none.
4. Run `make check` after doc updates to ensure nothing is broken.

## Documentation Style

Write every document for its reader, not for the code's authors. These rules
apply to every README and to every document in `docs/`, new or rewritten.

**READMEs** (the root `README.md`, and the `README.md` of a nested module or
an example) introduce; they do not explain internals. Each one, in order:

1. Says clearly **what the thing is, what it does and what problem it
   solves**, in a few sentences a newcomer understands.
2. Where appropriate, gives a **short example** of how to use it: a minimal
   code snippet or the command to run, and what to expect.
3. Points to **where to read more** (the user guide, the reference docs, the
   examples), and, for the root README, **how to install** and **how to
   develop**.

Implementation details a reader does not need in order to use the thing —
internal package names, protocol or wire details, spec and requirement ids,
errata, test names, the history of a decision — do **not** belong in a README.
Put them in `docs/` (`docs/architecture.md`, `docs/configuration.md`, or a
topic file such as `docs/{topic}.md`) and link to them.

**Reference documents** that cover a long list of things (`docs/api.md`,
`docs/cli.md`, `docs/configuration.md`, `docs/architecture.md`, and any other
document with more than a handful of sections) open with a short statement of
what the document covers and who it is for, followed by a **Contents** index
of links to its sections, so a reader can jump straight to what they need.
Keep the index in step with the headings whenever a section is added,
renamed or removed.

**General rules:**

- Lead with purpose, then usage, then detail.
- Every code snippet must compile against the current API and run as shown.
- Prefer one canonical place for each fact and link to it, rather than
  repeating it; when a fact must appear twice, keep both copies in step.
- Archived and historical records (`docs/prd/`, `docs/errata/`,
  `docs/archive/`, `.specs/`) are not rewritten to follow these rules.

## Generated Docs

`make doc` writes `go doc -all` output for every publicly accessible package of
the repository (no `internal/` packages, no `examples/`) to `docs/generated/`,
one text file per package. The files are committed to git.

Their purpose is orientation: **when you start working on this repository, use
`docs/generated/` to find your way around.** It is the cheapest way to see which
packages exist, what each one is for and which types, functions and constants
it exports, before reading any source. Prefer it to scanning directories or
grepping for declarations.

- Never edit these files by hand; they are overwritten by `make doc`.
- Treat them as possibly stale: the code is authoritative. If a file disagrees
  with the source, regenerate it.
- Run `make doc` and commit the result whenever a change adds, removes or
  alters an exported identifier or a package doc comment.

## Repository Specifics

AgentKit is a library with no REST API: `docs/api.md` is the map of its public
Go packages. Beyond the four reference docs it keeps `docs/guide.md` (the user
guide: each feature with an example), `docs/development.md` (building,
testing, make targets) and `docs/releasing.md`; a change to a feature's usage
updates its section of the guide, and a change to the make targets or the
test setup updates `docs/development.md`. The nested modules are listed in `MODULES` in the `Makefile`;
`make test`, `make vet`, `make lint` and `make tidy` run in each of them.
The shared test helpers are `internal/testutil` (file trees, the hermetic git
environment, polling, JSON, tar, golden files, `go run`; standard library only,
so every module's tests can import it) and, for doubles over `core`,
`internal/testkit`.
