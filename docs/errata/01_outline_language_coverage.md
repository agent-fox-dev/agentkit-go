# Erratum: outline's language coverage outside Go

**Relates to:** spec 01 (`.specs/01_outline_and_walk`) — PRD §1, §3, §4 and
Design Decisions 14 and 16.
**Status:** raised in
[agent-fox-dev/agentkit-go#73](https://github.com/agent-fox-dev/agentkit-go/issues/73).

## What the spec said

- §1: the extension table holds the ten heuristic languages "plus a short list
  of further extensions universal-ctags handles well"; an extension outside it
  is `none` and the file is not read.
- §3: "a function-kind tag whose scope kind is type-like (class, struct,
  interface, enum, trait, impl) becomes `method` with `Container` set to the
  scope", and a batch whose output is not usable falls back to the heuristic.
- §4: heuristic declarations start at column 0; `Container` is empty.

## What was delivered, and why

**ctags methods.** Universal Ctags 6.2 reports a method as kind `method`, not
`function`, in Java, C#, JavaScript, TypeScript, Ruby, Kotlin, Rust and
Objective-C; only C++, PHP and Python use `function` or `member`. The rule
applied only to function-kind tags, so every `method` tag kept an empty
`Container` and a qualified `find_symbol` (`Runner.run`) could never match it.
Both kinds now become a method of a type-like scope. The type-like scope kinds
add `implementation` (a Rust `impl`, an Objective-C `@implementation`),
`service` (a Protobuf or Thrift `rpc`), `object` and `protocol`, and, in
Ruby only, `module`. A `method` tag with no type around it — Kotlin and
GDScript report top-level functions that way — is a `func`.

**Kinds.** Kinds are mapped per language where parsers disagree on a word:
Rust's `interface` is a trait, Objective-C's `interface` is a class, Kotlin's
`object` is a class (the JSON parser's `object` is not a declaration), and
Terraform's `resource`, `data` and `output` blocks are `var`. `alias`,
`typealias`, `message`, `record`, `table` and `view` are types;
`singletonMethod` and `rpc` are methods; `generator`, `subprogram` and
`subprogspec` are functions; `protocol` and `service` are interfaces;
`packspec` is a module.

**Empty ctags output.** A file ctags returns no declarations for — it has no
parser for the extension (`.tsx`, `.mts`, `.cts`, `.cjs`, `.kts`), or every
tag it emits is a kind the closed set drops — is "output not usable" for that
file. It gets the heuristic's outline when the heuristic finds something, and
stays a `ctags` outline with no declarations otherwise. The argument list
stays as §3 pins it, so ctags is not told about those extensions.

**The extension table.** It now lists the programming languages Universal
Ctags 6.2 has a parser for, by the extensions ctags maps to them: Ada,
Clojure, COBOL, CUDA, D, Elixir, Elm, Emacs Lisp, Erlang, Fortran, GDScript,
Julia, Lisp, OCaml, Objective-C (`.mm`), Pascal, PowerShell, Protocol
Buffers, R, Raku, Scheme, SQL, SystemVerilog, Tcl, Terraform, Thrift, VHDL
and Vim script. Left out: ambiguous extensions (`.m`, `.v`, `.s`), markup and
data formats (their tags are headings and keys), and Haskell, whose parser
reports type constructors as functions and misses ordinary functions. Dart,
Zig, Vue and Svelte have no Universal Ctags 6.2 parser and are not listed. An
extension outside the table is still `none` and unread (Design Decision 16).

**`.h` headers.** `.h` stays C in the table, but `LangFor(path, src)` returns
C++ for a header whose content declares a class or namespace, a template, an
access specifier, `using namespace` or an `enum class`. `File.Lang`, the
heuristic's rule set and the code search index's `lang:` use it.

**Heuristic declarations not at column 0.** In C# and C++, the body of a
`namespace X { … }` block counts as top level at its own indent, so an
ordinary C# file, whose types are all indented one level in a block-scoped
namespace, is no longer an empty outline.

**Heuristic `Container`.** A C++ out-of-class definition (`void Foo::bar()`,
`Foo::Foo()`, `ns::Widget::name()`) is a `method` with `Container` set to the
qualifying class (`Foo`, `ns::Widget`), as ctags would report it. The source
line names the class, so the container is not a guess. Every other heuristic
declaration still has an empty `Container`.

**Heuristic forms.** The rules now cover modifiers and forms ordinary code
uses: Kotlin `sealed`/`open`/`abstract`/`data` classes, `private`/`internal`/
`suspend`/`override` functions, generic and extension functions, `object`,
`fun interface` and `typealias`; TypeScript `abstract`/`declare` classes,
`type` aliases, `const enum`, and `const`/`let`/`var` bound to an arrow
function or function expression (also in JavaScript, with generators); Rust
`const`/`async`/`unsafe`/`extern` functions and `macro_rules!`; Java's full
modifier set, `record` and `@interface`; C# `partial`, `readonly`, `record`
and namespaces; Ruby `def self.name` and names ending in `?`, `!` or `=`;
C++ `enum class`, return types with `::` or templates; C and C++ `typedef`,
including function pointers and multi-line `typedef struct { … } Name;`.
