# Erratum: spec 26 `provider_catalog_tiers`

The places where the delivered code and its tests differ from the text of
`.specs/26_provider_catalog_tiers`, and why. PRD 14 and the spec are not edited.
In each case the test checks what the code delivers.

## The package imports core, not "only core" in `go list -deps` (26-REQ-9.4, TS-26-34)

TS-26-34 says the only non-standard-library package in the dependency set of
`catalog` is this module's `core`, and its pseudocode asserts
`d == modulePath+"/core"` for every dependency. `go list -deps ./catalog` also
names `github.com/agent-fox-dev/agentkit-go/schema`, because `core` imports
`schema`; `catalog` reaches it through `core` and does not import it. The
criterion itself says the package imports only `core` and the standard library,
which is about its own imports.

The test checks that (`TestPackageImportsOnlyCore_TS26_34`,
`catalog/select_guarantees_test.go:207`): every import of a non-test file is the
standard library or `core`, and every non-standard dependency is `core`, `catalog`
or a package `core` itself depends on. It also checks that `resolve.go` and
`clamp.go` do not exist. An import of `wire` or `schema` from `catalog` fails it.

## The faux request carries no model (26-PATH-4, TS-26-39)

TS-26-39 asserts `req.Model.ID == "claude-sonnet-5-5"` and
`req.Model.MaxOutputTokens == preSpecMax` on the request the faux provider
received. `core.Request` has no `Model` field (`core/provider.go:14`): the model
is the agent's, resolved once in `agentkit.New` (`agent.go:152`; since spec 29 by
`Resolve`, through `catalog.Select`, which reaches `Lookup` for an Anthropic id and
returns the same row) and used for the
cap, the pricing and the model name stamped on messages (`loop.go:411`,
`loop.go:489`). The cap sent in the request is `Config.MaxTokens`, or
`DefaultMaxTokens` (`loop.go:438`), and the provider caps it at the model's.

The test reads the same facts where the code puts them
(`TestSmokeAgentNewStillResolvesThroughLookup_TS26_39`,
`catalog_tiers_smoke_test.go:21`): the model `New` resolved equals
`catalog.Lookup("claude-sonnet-5-5")` and has the window 1,000,000, the output
cap 128,000 and the prices 2 and 10 the row had before this spec; the request
carries `MaxTokens` 32,768 (`DefaultMaxTokens`); the assistant message names
`claude-sonnet-5-5`; and the run's cost is 12 USD for one million input and one
million output tokens, which the loop priced at the row because the faux
provider did not.

## The sentinels' text and the wrapping format (26-REQ-6.6, task 4 step 3)

26-REQ-6.6 says `Select` wraps the three sentinels with `fmt.Errorf("%w: …")`,
and task 4 gives `"%w: unknown provider %q; known providers are anthropic,
openrouter and ollama"` while also requiring each sentinel's text to start with
`agentkit:`. Together they would print `agentkit: …: unknown provider "openai"; …`.
TS-26-3 and 26-REQ-1.4 pin the message exactly:
`agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama`.

The sentinels carry the first words of the message (`agentkit: unknown provider`,
`agentkit: unknown effort`, `agentkit: no model`; `catalog/select.go:62-66`),
and `Select` wraps them with `"%w %q; …"` (`catalog/select.go:136`, `151`, and
`109` for the model). `errors.Is` matches each sentinel and no other, the text of
each starts with `agentkit:`, and the messages are:

- `agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama`
- `agentkit: unknown effort "turbo"; known efforts are low, medium, high, xhigh and max`
- `agentkit: no model given; name a tier (SIMPLE, STANDARD or ADVANCED); a model id is also accepted`

`TestSelectUnknownProvider_TS26_3` (`catalog/select_test.go:30`) pins the first
exactly; `TestSelectNormalizesEfforts_TS26_22` (`:213`) and
`TestSelectNoModel_TS26_23` (`:245`) check the others against 26-REQ-6.4 and
26-REQ-6.5.

## "Every successful result has a positive window and output cap" holds for rows that have limits (26-REQ-8.4)

The PRD's guarantee list says every successful result has a positive window and
output cap, and 26-REQ-8.4 limits it to "a valid catalog". `Parse` accepts a row
with no `context_window` or `max_tokens`, as it did before this spec: it rejects
only a negative one (`catalog/catalog.go:355`, `358`), and
`TestUnknownAPIStringLoads` loads a row `{}`. `Select` returns a listed row as
the catalog has it (`catalog/select.go:205`), so a catalog given to `Parse`
whose row has a zero limit gives a Selection with a zero limit. `Lookup` behaves
the same.

Every row of the embedded catalog and of the catalogs the tests use has positive
limits, and every fallback does. `TestSelectIsTotal_TS26_31` checks the guarantee
over them (3,000 random inputs each). Neither `Parse` nor `Select` was changed to
enforce it, because that would change what `Parse` accepts and `Lookup` returns.
