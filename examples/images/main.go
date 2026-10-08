// Command images shows how images get to a model: in a user message, from the
// built-in read_file tool, and from a tool of your own — and how imagex keeps
// every one of them inside the providers' inline-image limits.
//
//	go run ./examples/images
//
// Everything runs with no API key and no network: the example draws its own
// 3200×1800 PNG, drives the real loop against a scripted faux provider, and
// builds one real wire request offline. With a credential, --real sends the
// same picture to a vision model and prints what it says it sees:
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./examples/images --real
//	AGENTKIT_MODEL=google/gemini-3.8-flash go run ./examples/images --real
//
// See examples/images/README.md for a walkthrough.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/catalog"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/imagex"
	"github.com/agentfox/agentkit-go/provider"
	"github.com/agentfox/agentkit-go/provider/anthropic"
	"github.com/agentfox/agentkit-go/provider/faux"
	"github.com/agentfox/agentkit-go/provider/google"
	"github.com/agentfox/agentkit-go/provider/ollama"
	"github.com/agentfox/agentkit-go/provider/openai"
	"github.com/agentfox/agentkit-go/provider/openairesponses"
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/stop"
	"github.com/agentfox/agentkit-go/tools"
)

func main() {
	real := flag.Bool("real", false, "also send the picture to a real vision model (needs a credential)")
	flag.Parse()
	if err := run(os.Stdout, *real); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, real bool) error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "agentkit-images-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// A picture larger than any provider accepts inline: 3200 px wide, where
	// the limit is 2000 on each side.
	raw := drawPicture(3200, 1800)

	// ------------------------------------------------------------------ 1
	section(w, "1. imagex on its own: sniff, validate, normalize")
	// Saved under a misleading name on purpose: format is decided by magic
	// bytes, never by the file extension.
	shot := filepath.Join(dir, "screenshot.txt")
	if err := os.WriteFile(shot, raw, 0o644); err != nil {
		return err
	}
	mime, ok := imagex.Sniff(raw[:16])
	fmt.Fprintf(w, "  Sniff(screenshot.txt)  → %s (image: %v), whatever the extension says\n", mime, ok)
	fmt.Fprintf(w, "  Validate               → %v\n", imagex.Validate(raw, mime))
	fmt.Fprintf(w, "  Validate(text bytes)   → %v\n", imagex.Validate([]byte("hello, world"), ""))
	fmt.Fprintf(w, "  FitsBudget(%d bytes) → %v (budget is %d bytes of BASE64)\n",
		len(raw), imagex.FitsBudget(len(raw)), imagex.MaxBase64Bytes)

	res, err := imagex.Normalize(raw, mime)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  Normalize              → %s %d×%d, changed=%v (max side %d)\n",
		res.MIMEType, res.Width, res.Height, res.Changed, imagex.MaxDimension)
	again, _ := imagex.Normalize(res.Data, res.MIMEType)
	fmt.Fprintf(w, "  Normalize(again)       → changed=%v: a conforming image is returned byte for byte\n",
		again.Changed)

	// ------------------------------------------------------------------ 2
	section(w, "2. an image in a user message")
	// The model must declare "image" in Input. A faux model declares only
	// "text", so say otherwise for this one; a catalog model already does.
	model := faux.Model()
	model.Input = []string{"text", "image"}
	p := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("A red square and a blue circle.")}})
	agent, err := newAgent(model, p)
	if err != nil {
		return err
	}
	// RunMessage, not Run: Run takes a string. Normalize FIRST — images in a
	// user message are not normalized for you (tool results are; see 3).
	msg := core.UserMessage{Content: core.Content{
		core.TextBlock{Text: "What is in this picture?"},
		core.ImageBlock{Data: res.Base64(), MimeType: res.MIMEType},
	}}
	out, err := agent.RunMessage(ctx, msg)
	if err != nil {
		return err
	}
	sent := p.Requests()[0].Messages[0].(core.UserMessage)
	for _, b := range sent.Content {
		fmt.Fprintf(w, "  sent block: %s\n", describe(b))
	}
	fmt.Fprintf(w, "  answer: %q\n", out.FinalText())

	// ------------------------------------------------------------------ 3
	section(w, "3. read_file returns an image, normalized")
	ws, err := tools.NewWorkspace(dir)
	if err != nil {
		return err
	}
	all, err := tools.All(tools.Options{Workspace: ws, DisableSpill: true})
	if err != nil {
		return err
	}
	p = faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("call_1", "read_file", `{"path":"screenshot.txt"}`)},
			StopReason: core.StopReasonToolUse},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("It is a test card.")}},
	)
	agent, err = newAgent(model, p)
	if err != nil {
		return err
	}
	for _, t := range all {
		if t.Name == "read_file" { // only the tool this section needs
			if err := agent.RegisterTool(t); err != nil {
				return err
			}
		}
	}
	if _, err := agent.Run(ctx, "Look at screenshot.txt"); err != nil {
		return err
	}
	printToolResults(w, agent)

	// ------------------------------------------------------------------ 4
	section(w, "4. your own tool: images are normalized at the history boundary")
	// This tool returns the ORIGINAL 3200×1800 picture. It does nothing about
	// size, and it does not have to: every image entering history from a
	// tool result is normalized once, by the loop, after AfterToolCall.
	camera := core.Tool{
		Name:        "take_snapshot",
		Description: "Capture the dashboard as an image.",
		InputSchema: schema.Object(),
		Execute: func(ctx context.Context, _ json.RawMessage) core.ToolResult {
			r := core.OKResult(map[string]any{"captured": true})
			r.Text = "[dashboard snapshot]"
			r.Blocks = []core.ContentBlock{core.ImageBlock{
				Data: base64.StdEncoding.EncodeToString(raw), MimeType: imagex.MIMEPNG,
			}}
			return r
		},
	}
	fmt.Fprintf(w, "  the tool returned: %s\n", describe(camera.Execute(ctx, nil).Blocks[0]))
	p = faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("call_1", "take_snapshot", `{}`)},
			StopReason: core.StopReasonToolUse},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("The dashboard looks healthy.")}},
	)
	agent, err = newAgent(model, p)
	if err != nil {
		return err
	}
	if err := agent.RegisterTool(camera); err != nil {
		return err
	}
	if _, err := agent.Run(ctx, "How does the dashboard look?"); err != nil {
		return err
	}
	printToolResults(w, agent)

	// ------------------------------------------------------------------ 5
	section(w, "5. a model without image input gets a placeholder")
	// Built offline with the real Anthropic request builder: no network.
	textOnly := &core.Model{ID: "text-only", API: anthropic.API, Provider: "anthropic",
		MaxTokens: 1024, Input: []string{"text"}}
	_, rep, err := anthropic.BuildRequest(textOnly, core.Request{Messages: core.Messages{msg}}, core.CacheRetentionNone)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  repairs: %s\n", rep)
	fmt.Fprintf(w, "  the image became the text %q\n", provider.ImagePlaceholder)
	fmt.Fprintln(w, "  The transcript keeps the image; only this request's view lost it.")

	// ------------------------------------------------------------------ 6
	section(w, "6. a real vision model")
	if !real {
		fmt.Fprintln(w, "  skipped: pass --real (and set a credential) to run it.")
		return nil
	}
	return realRun(ctx, w, msg)
}

// realRun sends the normalized picture to a catalog model, which declares
// image input itself.
func realRun(ctx context.Context, w io.Writer, msg core.UserMessage) error {
	model, err := catalog.ResolveModel(modelSpec())
	if err != nil {
		return err
	}
	if !model.SupportsImages() {
		return fmt.Errorf("%s does not declare image input in the catalog", model.ID)
	}
	if err := checkCredentials(model); err != nil {
		return err
	}
	cfg := core.AgentConfig{Model: model}
	agentkit.RegisterDefaults(&cfg,
		anthropic.Provider(anthropic.Options{}),
		openai.Provider(openai.Options{}),
		openairesponses.Provider(openairesponses.Options{}),
		google.Provider(google.Options{}),
		ollama.Provider(ollama.Options{}),
	)
	cfg.StopPolicy = stop.Any(stop.AfterTurns(2), stop.OverBudget(0.10))
	cfg.SystemPrompt = "Describe images plainly, in one sentence."
	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	res, err := agent.RunMessage(ctx, msg)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  %s: %s\n", model.ID, strings.TrimSpace(res.FinalText()))
	fmt.Fprintf(w, "  [in %d / out %d tokens · $%.5f]\n",
		res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CostUSD)
	return nil
}

func newAgent(model *core.Model, p *faux.Provider) (*agentkit.Agent, error) {
	return agentkit.NewAgent(core.AgentConfig{
		Model:        model,
		SystemPrompt: "You are a helpful assistant.",
		StopPolicy:   stop.AfterTurns(4),
		Providers:    core.ProviderRegistry{faux.API: p.APIProvider()},
		// An image that cannot be normalized is KEPT (the run continues) and
		// reported here, as an *agentkit.ImageNormalizationError.
		Hooks: core.Hooks{OnError: func(err error) { fmt.Fprintln(os.Stderr, "hook:", err) }},
	})
}

// printToolResults shows what entered history from each tool call.
func printToolResults(w io.Writer, agent *agentkit.Agent) {
	for _, m := range agent.History().Messages() {
		tr, ok := m.(core.ToolResultMessage)
		if !ok {
			continue
		}
		for _, b := range tr.Content {
			fmt.Fprintf(w, "  in history (%s): %s\n", tr.ToolName, describe(b))
		}
	}
}

// describe renders a content block in one line, decoding an image's header to
// report its real dimensions.
func describe(b core.ContentBlock) string {
	switch v := b.(type) {
	case core.TextBlock:
		return fmt.Sprintf("text %q", v.Text)
	case core.ImageBlock:
		data, err := imagex.DecodeBase64(v.Data)
		if err != nil {
			return "image (undecodable)"
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return "image (no header)"
		}
		return fmt.Sprintf("image %s %d×%d, %d KiB of base64", v.MimeType, cfg.Width, cfg.Height, len(v.Data)/1024)
	}
	return string(b.BlockType())
}

// drawPicture draws a test card a vision model can describe: a red square and
// a blue circle on white.
func drawPicture(wd, ht int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, wd, ht))
	white := color.RGBA{255, 255, 255, 255}
	red := color.RGBA{220, 30, 30, 255}
	blue := color.RGBA{30, 60, 220, 255}
	cx, cy, r := wd*3/4, ht/2, ht/4
	for y := 0; y < ht; y++ {
		for x := 0; x < wd; x++ {
			c := white
			if x > wd/8 && x < wd/8+ht/2 && y > ht/4 && y < ht*3/4 {
				c = red
			}
			if dx, dy := x-cx, y-cy; dx*dx+dy*dy < r*r {
				c = blue
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n── %s %s\n", title, strings.Repeat("─", max(0, 70-len(title))))
}

// modelSpec is "vendor/model-id", or a bare id when it is unambiguous.
func modelSpec() string {
	if s := os.Getenv("AGENTKIT_MODEL"); s != "" {
		return s
	}
	return "anthropic/claude-sonnet-5"
}

// checkCredentials fails BEFORE the request with a message naming the variable
// to set, rather than after a 401 that names none of them. "ambient" (an
// instance role, ADC) passes; only "none" fails.
func checkCredentials(m *core.Model) error {
	auth := provider.ResolveAuth(authFor(m), provider.Env{})
	if auth.State != provider.CredentialNone {
		return nil
	}
	return fmt.Errorf("no credential for vendor %q: set one of %s (see examples/README.md)",
		m.Provider, strings.Join(varNames(authFor(m)), ", "))
}

func authFor(m *core.Model) provider.VendorAuth {
	switch m.API {
	case anthropic.API:
		return anthropic.VendorAuth
	case google.API:
		return google.VendorAuth
	case ollama.API:
		return ollama.VendorAuth
	default:
		return openai.AuthFor(m.Provider)
	}
}

func varNames(v provider.VendorAuth) []string {
	out := make([]string, 0, len(v.Vars)+1)
	for _, e := range v.Vars {
		out = append(out, e.Name)
	}
	if v.BaseURLVar != "" {
		out = append(out, v.BaseURLVar+" (for a gateway or a local server)")
	}
	if len(out) == 0 {
		out = append(out, "a vendor-specific API key")
	}
	return out
}
