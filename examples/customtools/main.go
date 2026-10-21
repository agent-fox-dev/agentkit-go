// Command customtools is the reference for writing your own tools: schemas
// built from combinators, the two handler shapes, error results the model can
// recover from, per-tool argument repair, sequential execution for tools with
// side effects, and a terminating submit_answer.
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./examples/customtools
//	go run ./examples/customtools "Reserve four BRKT-90 and tell me their total mass in pounds."
//
// The scenario is a tiny parts inventory. Four tools cover it end to end:
// lookup_part reads records, convert_units does arithmetic the model should
// not do in its head, reserve_stock mutates shared state, and submit_answer
// ends the run.
//
// AGENTKIT_PROVIDER, AGENTKIT_MODEL and AGENTKIT_EFFORT choose what runs the
// model: the provider (anthropic, the default, openrouter or ollama), a tier
// (SIMPLE, STANDARD, the default, or ADVANCED) or the provider's own model id,
// and the effort (low to max; the tier's when empty). One run on each provider:
//
//	ANTHROPIC_API_KEY=sk-ant-... go run ./examples/customtools
//	AGENTKIT_PROVIDER=openrouter OPENROUTER_API_KEY=sk-or-... go run ./examples/customtools
//	AGENTKIT_PROVIDER=ollama AGENTKIT_MODEL=SIMPLE AGENTKIT_EFFORT=high go run ./examples/customtools
//
// See examples/README.md for the full environment-variable table.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Reserve three BOLT-M6 and one BRKT-90, then tell me the total mass of that reservation in ounces."
	}

	// 1. The tools. New rejects a tool that sets both Handler and Execute, or
	//    neither: the two shapes are alternatives, not layers, and catching
	//    that at construction beats discovering it when the model first
	//    calls the tool.
	//
	//    submit_answer writes the finished answer into a variable this
	//    function owns. That is the reason to have a terminating tool at all
	//    rather than scraping res.FinalText(): the answer arrives as
	//    structured arguments the schema already validated.
	var submitted answer

	// 2. The provider, the model and the effort are the three settings that
	//    choose what runs the agent, and the agent does the rest: New resolves
	//    the model, a tier or an id, through the catalog, which supplies the
	//    context window, output cap, pricing and how the model takes extended
	//    thinking (an id it does not list still works, with default limits
	//    and no price), and builds the client of the provider from the
	//    environment. Nothing is registered by import side effect. An unknown
	//    provider or effort, or a missing key, fails here, before any request,
	//    by name or by the variable to set, rather than as a 401 that names
	//    none of them.
	//
	//    A tool-using run needs an upper bound that does not depend on the
	//    model choosing to stop. submit_answer is the *intended* ending;
	//    MaxTurns and MaxCostUSD are what happens when the model never gets
	//    there.
	provider, model, effort := selection()
	agent, err := agentkit.New(agentkit.Config{
		Provider: provider,
		Model:    model,
		Effort:   effort,
		System:   "You are a parts-desk assistant. Use the tools rather than guessing part data.",
		Toolset: agentkit.Toolset{Tools: []core.Tool{
			lookupPartTool(),
			convertUnitsTool(),
			reserveStockTool(),
			submitAnswerTool(&submitted),
		}},
		MaxTurns:   12,
		MaxCostUSD: 1.00, // dollars, cumulative for the run
	})
	if err != nil {
		return err
	}

	// 3. Stream, so the tool calls are visible as they happen. The tool
	//    events are the interesting ones here: ToolCallStartEvent is the
	//    model's intent, ToolExecutionEndEvent is what your handler actually
	//    returned — including whether it came back as an error result.
	stream, err := agent.Stream(context.Background(), prompt)
	if err != nil {
		return err
	}
	for event := range stream.Events() {
		switch e := event.(type) {
		case core.TextDeltaEvent:
			fmt.Print(e.Delta)
		case core.ToolCallStartEvent:
			fmt.Printf("\n  → %s", e.Name)
		case core.ToolCallEndEvent:
			// The finalized call carries the model's own argument bytes,
			// before the argument pipeline touched them — so a repair made by
			// PrepareArguments is visible as the difference between what is
			// printed here and what the handler received.
			fmt.Printf("(%s)\n", compact(e.Block.Input))
		case core.ToolExecutionEndEvent:
			status := "ok"
			if e.IsError {
				status = "error result"
			}
			fmt.Printf("  ← %s: %s (%dms)\n", e.Name, status, e.ElapsedMS)
		case core.ErrorEvent:
			fmt.Fprintf(os.Stderr, "\n[stream error: %s]\n", e.Message)
		}
	}

	// A run that hit MaxTurns or MaxCostUSD ends with core.ErrMaxTurns or
	// core.ErrBudgetExceeded. That is an outcome to report, not a crash, so
	// it falls through to the "ended without submit_answer" line below.
	res, err := stream.RunResult()
	if err != nil && !errors.Is(err, core.ErrMaxTurns) && !errors.Is(err, core.ErrBudgetExceeded) {
		return err
	}

	// 4. RunStopToolTerminate is a distinct outcome from "the model stopped
	//    talking". Seeing it means submit_answer ran and the loop ended on
	//    purpose, which is the difference between an answer and a transcript
	//    that happens to end.
	fmt.Println()
	if res.StopReason == core.RunStopToolTerminate {
		fmt.Printf("answer: %s\n", submitted.Text)
		if len(submitted.Parts) > 0 {
			fmt.Printf("cited:  %s\n", strings.Join(submitted.Parts, ", "))
		}
	} else {
		fmt.Printf("run ended without submit_answer (%s)\n", res.StopReason)
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", summary(agent.Resolution(), res))
	return nil
}

// ---------------------------------------------------------------- inventory

type part struct {
	Name      string
	Material  string
	MassGrams float64
	OnHand    int
}

// inventory is deliberately an unsynchronized map. Reads happen from
// lookup_part, which runs in parallel with its batch peers; concurrent reads
// of a map are safe. The only writer is reserve_stock, and that tool declares
// ExecutionMode: core.Sequential, which is what keeps the write off any other
// goroutine. See the comment on reserveStockTool.
var inventory = map[string]*part{
	"BOLT-M6": {Name: "M6 hex bolt, 30mm", Material: "stainless", MassGrams: 12.4, OnHand: 480},
	"NUT-M6":  {Name: "M6 nylon-insert nut", Material: "stainless", MassGrams: 3.1, OnHand: 512},
	"BRKT-90": {Name: "90-degree mounting bracket", Material: "aluminium", MassGrams: 86.0, OnHand: 24},
	"WSHR-M6": {Name: "M6 flat washer", Material: "stainless", MassGrams: 1.2, OnHand: 1500},
}

// ------------------------------------------------------------- lookup_part

// lookupPartTool is the Handler shape: json.RawMessage in, json.RawMessage
// out. Use it whenever the tool only needs to answer — it cannot reach
// Terminate, Metadata or image Blocks, because none of those fit through
// JSON bytes. A non-nil error from a Handler is not a crash either: the loop
// turns it into an error result with the code "handler_error".
func lookupPartTool() core.Tool {
	return core.Tool{
		Name:        "lookup_part",
		Description: "Look up one or more parts by id and return name, material, unit mass and stock on hand",

		// The schema is a VALUE, not a json.RawMessage, and everything below
		// depends on that. AgentKit rewrites it into each provider's dialect
		// (Anthropic input_schema, OpenAI parameters, and the strict subset
		// when a tool asks for constrained sampling); it coerces incoming
		// arguments against the declared types; it validates them and, on
		// failure, renders the declared shape into an error the model reads
		// and corrects. A pre-serialized blob supports none of those — it can
		// only be forwarded.
		//
		// PropertyOrder is carried too, which a map cannot do: Go marshals
		// maps with sorted keys, and property order is model-visible.
		InputSchema: schema.Object(
			schema.Prop("ids", schema.Array(schema.String(), "Part ids, e.g. BOLT-M6").MinItemsN(1)),
			schema.Opt("include_specs", schema.Bool("Include material and unit mass")),
		),

		// PromptGuidelines are folded into the assembled system prompt and
		// deduplicated preserving first-seen order. They live on the tool
		// rather than in a prompt template because the advice and the tool
		// ship, move and get deleted together: a guideline in a separate
		// prompt file outlives the tool it describes, and then the model is
		// told to use something that is not in its tool list.
		PromptGuidelines: []string{
			"Look parts up with lookup_part before quoting any part's mass or stock; do not recall them from memory.",
		},

		// PrepareArguments is a per-tool repair shim for shapes models
		// actually get wrong. It runs FIRST in the argument pipeline, ahead of
		// null-stripping, coercion and validation — so a repaired call is
		// validated as repaired, and a shape that is merely awkward never
		// costs a round trip. It must return a copy: the map it is handed is
		// folded back into the order-preserving form the handler is
		// regenerated from, and mutating it in place makes the repair
		// invisible to that comparison.
		//
		// The failure this fixes is a real one: a model that has decided the
		// call is about a single part writes "ids": "BOLT-M6", and a model
		// splitting a list in prose writes "ids": "BOLT-M6, NUT-M6". Both are
		// strings where an array was declared. Coercion will not do this for
		// you — it only converts primitives, and inventing an array from a
		// scalar is a guess that belongs to the tool that knows its own
		// vocabulary.
		PrepareArguments: func(in map[string]any) map[string]any {
			out := make(map[string]any, len(in))
			for k, v := range in {
				out[k] = v
			}
			switch v := out["ids"].(type) {
			case string:
				var ids []any
				for _, s := range strings.Split(v, ",") {
					if s = strings.TrimSpace(s); s != "" {
						ids = append(ids, s)
					}
				}
				out["ids"] = ids
			case map[string]any:
				// A bare object where a one-element array was declared:
				// {"id": "BOLT-M6"} instead of ["BOLT-M6"].
				if id, ok := v["id"].(string); ok {
					out["ids"] = []any{id}
				}
			}
			return out
		},

		Handler: func(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
			var args struct {
				IDs          []string `json:"ids"`
				IncludeSpecs bool     `json:"include_specs"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return nil, err
			}
			found := make([]map[string]any, 0, len(args.IDs))
			var missing []string
			for _, id := range args.IDs {
				p, ok := inventory[strings.ToUpper(strings.TrimSpace(id))]
				if !ok {
					missing = append(missing, id)
					continue
				}
				rec := map[string]any{"id": id, "name": p.Name, "on_hand": p.OnHand}
				if args.IncludeSpecs {
					rec["material"] = p.Material
					rec["mass_grams"] = p.MassGrams
				}
				found = append(found, rec)
			}
			// A partial answer is more useful than a failure: the model gets
			// the parts that exist plus the ids that did not, and fixes only
			// the bad ones. Reporting the whole call as an error would throw
			// away work the model then has to ask for again.
			return json.Marshal(map[string]any{
				"parts": found, "missing": missing, "known_ids": knownIDs(),
			})
		},
	}
}

func knownIDs() []string {
	ids := make([]string, 0, len(inventory))
	for id := range inventory {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ----------------------------------------------------------- convert_units

var unitScale = map[string]struct {
	dimension string
	perBase   float64 // base units: grams for mass, millimetres for length
}{
	"g":  {"mass", 1},
	"kg": {"mass", 1000},
	"oz": {"mass", 28.349523125},
	"lb": {"mass", 453.59237},
	"mm": {"length", 1},
	"cm": {"length", 10},
	"m":  {"length", 1000},
	"in": {"length", 25.4},
	"ft": {"length", 304.8},
}

// convertUnitsTool is the Execute shape: it returns a core.ToolResult instead
// of bytes. That is the only way to reach Terminate, Metadata and image
// Blocks — and it is also what lets a tool distinguish "this call failed" from
// "this call crashed".
func convertUnitsTool() core.Tool {
	return core.Tool{
		Name:        "convert_units",
		Description: "Convert a quantity between units of mass (g, kg, oz, lb) or length (mm, cm, m, in, ft)",

		// Enum is the combinator that earns its keep here: it pins the
		// vocabulary in the schema, so a wrong unit is usually rejected before
		// it ever reaches this process. What the schema cannot express is the
		// relationship BETWEEN two fields — that "from" and "to" must share a
		// dimension — which is exactly why the handler still checks.
		InputSchema: schema.Object(
			schema.Prop("value", schema.Number("The quantity to convert")),
			schema.Prop("from", schema.Enum("Unit to convert from", "g", "kg", "oz", "lb", "mm", "cm", "m", "in", "ft")),
			schema.Prop("to", schema.Enum("Unit to convert to", "g", "kg", "oz", "lb", "mm", "cm", "m", "in", "ft")),
			schema.Opt("decimals", schema.Int("Round to this many decimal places").Min(0).Max(6)),
		),
		PromptGuidelines: []string{
			"Convert units with convert_units rather than doing the arithmetic in prose.",
		},

		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var args struct {
				Value    float64 `json:"value"`
				From     string  `json:"from"`
				To       string  `json:"to"`
				Decimals *int    `json:"decimals"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}
			from, to := unitScale[args.From], unitScale[args.To]

			// An error result is NOT a Go error. The loop appends it to the
			// transcript as a tool result with is_error set and keeps going,
			// so the model reads the message and corrects itself on the next
			// turn. That makes the wording load-bearing: name what was wrong
			// and what would be right, because this text is the entire repair
			// instruction the model gets.
			if from.dimension != to.dimension {
				return core.ErrResult("dimension_mismatch", fmt.Sprintf(
					"cannot convert %s (%s) to %s (%s); mass units are g, kg, oz, lb and length units are mm, cm, m, in, ft",
					args.From, from.dimension, args.To, to.dimension))
			}
			out := args.Value * from.perBase / to.perBase
			if args.Decimals != nil {
				out = round(out, *args.Decimals)
			}
			return core.OKResult(map[string]any{
				"value": out, "unit": args.To, "dimension": from.dimension,
			})
		},
	}
}

func round(v float64, places int) float64 {
	f := math.Pow(10, float64(places))
	return math.Round(v*f) / f
}

// ----------------------------------------------------------- reserve_stock

// reserveStockTool mutates process-wide state, which is what ExecutionMode
// decides.
//
// Parallel is the zero value and the right default for a tool that only reads.
// Sequential is for a tool whose effects are observable outside its own call —
// a write to shared memory, a file in the workspace, a row in a database. Two
// such calls running concurrently produce an outcome that depends on which
// goroutine won, and a batch that reserves the last four brackets twice is
// worse than a batch that fails the second reservation honestly.
//
// One Sequential tool demotes the WHOLE batch: if the model emits
// reserve_stock alongside two lookup_part calls, all three run in order, on
// one goroutine, in the order the model wrote them. That is a deliberate
// trade — correctness of the side effect over the latency of its peers — and
// the reason not to mark a read-only tool Sequential out of caution.
func reserveStockTool() core.Tool {
	return core.Tool{
		Name:          "reserve_stock",
		Description:   "Reserve a quantity of a part, decrementing the stock on hand",
		ExecutionMode: core.Sequential,
		InputSchema: schema.Object(
			schema.Prop("id", schema.String("Part id to reserve")),
			schema.Prop("quantity", schema.Int("How many units to reserve").Min(1)),
		),
		PromptGuidelines: []string{
			"Reserve stock only once per part; reserve_stock is not idempotent and a repeated call double-books.",
		},
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			start := time.Now()
			var args struct {
				ID       string `json:"id"`
				Quantity int    `json:"quantity"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}
			p, ok := inventory[strings.ToUpper(strings.TrimSpace(args.ID))]
			if !ok {
				return core.ErrResult("unknown_part", fmt.Sprintf(
					"no part %q; known ids are %s", args.ID, strings.Join(knownIDs(), ", ")))
			}
			if p.OnHand < args.Quantity {
				return core.ErrResult("insufficient_stock", fmt.Sprintf(
					"%s has %d on hand, %d requested; reserve %d or fewer",
					args.ID, p.OnHand, args.Quantity, p.OnHand))
			}
			p.OnHand -= args.Quantity

			res := core.OKResult(map[string]any{
				"reserved": args.Quantity, "id": args.ID, "remaining": p.OnHand,
				"reserved_mass_grams": float64(args.Quantity) * p.MassGrams,
			})
			// Metadata never reaches the model — ToLLMMap strips it — so it is
			// where a tool reports to YOUR instrumentation without spending
			// tokens on it. It is reachable from Execute only.
			res.Metadata = &core.ToolMetadata{
				DurationMS: time.Since(start).Milliseconds(), Outcome: "ok",
			}
			return res
		},
	}
}

// ----------------------------------------------------------- submit_answer

type answer struct {
	Text  string
	Parts []string
}

// submitAnswerTool ends the run.
//
// ToolResult.Terminate is json:"-" — it never reaches the model. It is a
// VOTE: when any call in the batch that ran casts it, the run ends after the
// whole batch has finished. Sibling calls still run and are recorded, but the
// model never sees their results, so submit_answer is best asked for alone.
func submitAnswerTool(out *answer) core.Tool {
	return core.Tool{
		Name:        "submit_answer",
		Description: "Submit the final answer and end the run. Call this alone, once you have everything you need.",
		InputSchema: schema.Object(
			schema.Prop("answer", schema.String("The final answer in prose, including any numbers you computed")),
			schema.Opt("parts", schema.Array(schema.String(), "Part ids the answer relies on")),
		),
		PromptGuidelines: []string{
			"Finish by calling submit_answer with the complete answer; do not just write it as text.",
		},

		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var args struct {
				Answer string   `json:"answer"`
				Parts  []string `json:"parts"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}
			*out = answer{Text: args.Answer, Parts: args.Parts}

			// Terminate is set on the result rather than passed to OKResult,
			// because it is not part of the payload: OKResult builds what the
			// model sees, and this field is what the LOOP sees.
			res := core.OKResult(map[string]any{"received": true})
			res.Terminate = true
			return res
		},
	}
}

// ------------------------------------------------------------------ helpers

func compact(raw json.RawMessage) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	if len(s) > 90 {
		return s[:87] + "..."
	}
	return s
}

// selection reads the three settings that choose what runs the agent, and
// nothing else decides it. They are the library's own: Config.Provider (empty is
// anthropic), Config.Model (a tier or a model id, STANDARD here when unset) and
// Config.Effort (empty is the tier's). They are passed on unchanged, so a wrong
// value is New's error and not this program's. Each example that calls a model
// repeats these lines rather than sharing them.
func selection() (provider, model string, effort agentkit.Effort) {
	model = os.Getenv("AGENTKIT_MODEL")
	if model == "" {
		model = "STANDARD"
	}
	return os.Getenv("AGENTKIT_PROVIDER"), model, agentkit.Effort(os.Getenv("AGENTKIT_EFFORT"))
}

// summary is the closing line: what ran, from Agent.Resolution, and what the run
// used. A run whose cost could not be known says so, instead of printing a figure
// that is not a price.
func summary(r agentkit.Resolution, res core.RunResult) string {
	u := res.Usage
	cost := fmt.Sprintf("$%.5f", u.CostUSD)
	if u.Unpriced {
		cost = "cost unknown"
	}
	effort := string(r.Effort)
	if effort == "" {
		effort = "none"
	}
	return fmt.Sprintf("[%s · %s · effort %s · %d turns · stop %s · in %d / out %d tokens · %s]",
		r.Provider, r.Model.ID, effort, res.TurnCount, res.StopReason, u.InputTokens, u.OutputTokens, cost)
}
