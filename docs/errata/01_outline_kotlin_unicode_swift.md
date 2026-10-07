# Erratum: Kotlin visibility, Unicode identifiers, the BOM, and Swift/Scala

**Relates to:** spec 01 (`.specs/01_outline_and_walk`) — PRD §1 and §4,
requirements.json's heuristic `Exported` rule, test_spec TS-01-39/40.
**Status:** implemented 2026-10-07, from
[agent-fox-dev/agentkit-go#89](https://github.com/agent-fox-dev/agentkit-go/issues/89).

## Kotlin `Exported`

**Spec:** "a missing `public` in Java, Kotlin and C#" makes a declaration not
exported.

**Delivered:** Kotlin's default visibility is public, and `public` is almost
never written. Under the spec's rule `fun topLevel() {}` and `class Plain` were
unexported, `file_outline` listed no declarations for an ordinary Kotlin file,
and `find_symbol` ranked every Kotlin symbol last. A Kotlin declaration is now
exported unless `private`, `internal` or `protected` is among its modifiers.
Java and C# keep the spec's rule. The `heuristic_kotlin.kt` fixture's
`packageFun`/`PackageClass` are exported, and it gained a `private` and an
`internal` declaration for the unexported case.

## Identifiers

**Spec:** examples such as `^def `, `^class `; identifiers were `\w`, which in
Go is `[0-9A-Za-z_]`.

**Delivered:** an identifier is letters, digits and underscore in any script
(`[\p{L}\p{N}_]`), plus `$` in JavaScript and TypeScript. `def café()` was
outlined as `caf` and `class Größe` as `Gr`; Go, outlined through `go/ast`, was
the only language without the problem. Ruby's trailing `?`, `!` and `=` were
already part of its rule.

## The UTF-8 byte-order mark

A UTF-8 BOM is not part of line 1 for the heuristic: it stood in front of the
first declaration and no anchored rule matched it, so a BOM-marked file lost
its first declaration (`go/parser` and `edit_file` already skipped it).

## Swift and Scala

**Spec:** §1 lists Swift and Scala among the "further extensions
universal-ctags handles well".

**Delivered:** Universal Ctags has no parser for either (6.2 lists neither in
`--list-languages`), and there is no heuristic, so with ctags installed a
`.swift` file came back as a `ctags` outline with zero declarations — a false
"0 declarations". Both are out of the extension table: their files are
`Lang: ""`, `Backend: "none"`, "language not recognised". `LangSwift` and
`LangScala` remain as names. Elixir (`.ex`, `.exs`) and Objective-C (`.mm`),
which the issue also raised, were already in the table.
