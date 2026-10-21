# Erratum: spec 28 `ollama_provider`

The places where the delivered code and its tests differ from the text of
`.specs/28_ollama_provider`, and why. PRD 14 and the spec are not edited. In
each case the test checks what the code delivers. Sections are added by the
task that delivers the behaviour they describe.

## The Ollama package differs from the spec's External APIs table (all unverified in the spec)

The table in the spec is from the PRD's probe of `api` v0.40.7. The module was
read at that version and probed against an `httptest` server before any code
used it. Where the real package differs, the provider follows the package. All
line references are to `github.com/ollama/ollama@v0.40.7`.

- **An error status with an empty body is not an error to `Chat`.** The spec
  says `Chat` returns `api.StatusError` for a status of 400 or more. `stream`
  (`api/client.go:170`) checks the status only inside its loop over the lines of
  the body (`:239`, `:245`), so a 404, a 500 or a 401 with an empty body
  returns `nil` after no chunk at all.
- **A 401 is `api.AuthorizationError`.** Not `api.StatusError`
  (`api/client.go:239`, `api/types.go:43`); it carries no message.
- **An error line is a plain error.** A line `{"error": …}` inside a 200
  becomes `errors.New(message)` (`:253`), and a line that is not JSON becomes
  `errors.New(line)` (`:236`). Neither has a type or a code.
- **A failure to connect is a `*net.OpError`** with `Op == "dial"`, a DNS failure
  included (it wraps a `*net.DNSError`). A connection reset while the body is
  read is also a `*net.OpError`, with `Op == "read"`.
- **The signing path.** `stream` calls `envconfig.UseAuth()` (`:184`), which
  reads `OLLAMA_AUTH` from the process environment (`envconfig/config.go:232`),
  and also signs when the host name is exactly `ollama.com`. With either it reads
  `~/.ollama/id_ed25519` (`auth/auth.go:53`), adds an `Authorization` header and
  a `ts` query parameter, and returns an error before sending if the key is
  missing. Probed with `OLLAMA_AUTH=1`: no key, the request fails with `open
  …/.ollama/id_ed25519: no such file or directory`; with a key, the server saw
  `POST /api/chat?ts=<unix>` with an `Authorization` header.
- **The line limit is 8,000,000 bytes.** `maxBufferSize = 8 * format.MegaByte`
  (`:168`), a megabyte being 1,000,000 bytes, and `Chat` allocates a buffer of
  that size for every call (`:219`). The spec and the PRD give about 512 KB.
- **Tool parameters.** `ToolFunctionParameters.Properties` has no `omitempty`
  (`api/types.go:483`): a schema without `properties` marshals as
  `"properties":null`. `Items` and `$defs` are `any`, so `minimum` or `format`
  inside an `items` schema survives the cut (probed: `"items":{"minimum":3,…}`);
  the keywords the type models for a property (`additionalProperties`,
  `format`, `minimum`) are dropped.
- **Arguments.** `ToolCallFunctionArguments` keeps the top-level key order
  (`api/types.go:238`, an ordered map), but a nested object comes back with its keys sorted,
  a number passes through `float64` (`12345678901234567890` is written back as
  `12345678901234567000`), and `<`, `>` and `&` are written as `<`, `>`
  and `&`. The spec accepts the number re-rendering (Design Decision 2).
- **Usage.** `Metrics.PromptEvalCachedCount` is a `*int` (`api/types.go:566`),
  so an absent count can be told from zero; `PromptEvalCount` and `EvalCount`
  are plain integers and cannot be. `ThinkValue` is an alias of
  `types/model.ThinkValue`.
- **`envconfig.Host` is more lenient than the spec's refusals.** Run on each
  value (`envconfig/config.go:22`): a port that is not a number from 0 to 65535
  is replaced with the default and only logged (`h:99999` is `http://h:11434`,
  `h:abc` too), port 0 is kept, any scheme is kept (`ftp://h` is
  `ftp://h:11434`, `HTTP://h` is `HTTP://h:11434`), `http://[::1` is
  `http://[::1]:80`, `http://` is `http://127.0.0.1:80`, and `user:pw@h` is the
  host `user` at port 11434.

## Policy tests restated for the new module (28-REQ-9, task 1)

- **`provider/ollama` leaves TS-09-1's absence list.** Spec 09's
  `TestDeletedPackagesAbsent_TS09_1` pinned `provider/ollama` as deleted, and
  `make check` failed on it (`provider/ollama still exists`) the moment the
  package existed. The spec does not name that pin. The entry left the list in
  `repository_cut_test.go` with a comment that points here, and every other
  entry stays. The package is not the NDJSON one PRD 09 deleted: it imports
  `github.com/ollama/ollama/api` and has none of its code.
- **`sandbox-go-list-m-all.14.txt` is not regenerated.** 28-REQ-9.2 lists it with
  the snapshots to regenerate, "as TS-15-40 requires" whole-file equality with
  the current sandbox snapshot. Spec 27 had already restated TS-15-40 as: every
  line 14 committed is still present, and the only lines added are named. The
  `.14` file is spec 14's baseline, and regenerating it would erase what the
  test compares against. It is kept as committed; `TestLandlockPromoted_TS15_40`
  (`internal/policy/landlock_dep_test.go:30`) reads its lines through
  `acceptedLine`, which applies `d1Upgrades` and then `upgradesBySpec28`, and
  accepts as added only the modules in `addedBySpec27` and `addedBySpec28`.
- **Two accepted-upgrade maps, not one.** The spec asks for one map next to
  `d1Upgrades`. A sandbox baseline line has to be moved forward and a root graph
  line cannot: the sandbox graph already holds `x/crypto v0.46.0` and the
  others from spec 14's D-1, so applying the root's `v0.43.0` would move a
  version backwards. `upgradesBySpec28` (`internal/policy/deps_test.go`) holds
  the one version the sandbox graph raised, `gopkg.in/check.v1`, and
  `TestRsyncDependency_TS14_42` and `TestLandlockPromoted_TS15_40` apply it.
  `rootUpgradesBySpec28` records the six versions the root graph raised, which
  `TestSandboxDepsPromoted_TS13_65` compares as a whole with the regenerated
  `go-list-m-all.txt`; `TestGraphTestsPassOnTheRegeneratedSnapshots_TS28_44`
  checks that each entry of both is the version its snapshot holds. TS-13-65's
  `grpc` and `protobuf` pins did not move and are not restated.
- **TS-26-35's pinned require list names eight more modules.**
  `requiredModules26` (`repository_cut_test.go`) gains the Ollama module and
  the seven indirect lines `go mod tidy` wrote for it, as spec 27 did for the
  OpenRouter SDK. The spec lists `repository_cut_test.go` in the task's
  touches but does not name this pin.
- **TS-28-44 passes before the module is in the graph.** It asserts that the four
  graph tests still exist and pass, and they passed on the unchanged tree. It
  went red once the graph changed and before they were restated (the child run
  failed with the `check.v1` and added-module lines of TS-14-42 and TS-15-40),
  and a mutation test shows it fails when a snapshot line is changed.
