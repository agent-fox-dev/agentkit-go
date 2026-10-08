# images

Models with vision accept images as content blocks. An image can reach a model
in three ways:

1. **In a user message.** You attach it to the prompt.
2. **From `read_file`.** The built-in tool detects an image file and returns it
   as an image, not as bytes.
3. **From a tool of your own.** A screenshot, a chart, a rendered page.

Providers limit inline images. AgentKit applies a 2000 px limit on each side
and a 4.5 MiB limit on the **base64** text. Some formats are refused
outright, such as CMYK JPEG and animated PNG. Once a rejected image is in the
history, the provider error repeats on every later request. The
[`imagex`](../../imagex) package catches this before it happens: it detects,
validates, downscales and re-encodes. The loop runs it on every image that
enters history from a tool.

The example runs with **no API key and no network**. It draws its own
3200×1800 test card, drives the real loop against a scripted
[`provider/faux`](../../provider/faux) provider, and builds one real
Anthropic request offline. With a credential, `--real` sends the same picture
to a vision model.

```bash
go run ./examples/images
go test ./examples/images/

export ANTHROPIC_API_KEY=sk-ant-...         # or any vision model, see examples/README.md
go run ./examples/images --real
AGENTKIT_MODEL=google/gemini-3.8-flash go run ./examples/images --real
```

## What it shows

```
── 1. imagex on its own: sniff, validate, normalize ──────────────────────
  Sniff(screenshot.txt)  → image/png (image: true), whatever the extension says
  Validate(text bytes)   → imagex: not a recognised image
  FitsBudget(127472 bytes) → true (budget is 4718592 bytes of BASE64)
  Normalize              → image/png 2000×1125, changed=true (max side 2000)
  Normalize(again)       → changed=false: a conforming image is returned byte for byte

── 2. an image in a user message ─────────────────────────────────────────
  sent block: text "What is in this picture?"
  sent block: image image/png 2000×1125, 79 KiB of base64

── 3. read_file returns an image, normalized ─────────────────────────────
  in history (read_file): text "[screenshot.txt: image/png image, downscaled to 2000×1125 for the provider's inline limit]"
  in history (read_file): image image/png 2000×1125, 79 KiB of base64

── 4. your own tool: images are normalized at the history boundary ───────
  the tool returned: image image/png 3200×1800, 165 KiB of base64
  in history (take_snapshot): image image/png 2000×1125, 79 KiB of base64

── 5. a model without image input gets a placeholder ─────────────────────
  repairs: 1 images replaced
  the image became the text "(image omitted: model does not support images)"

── 6. a real vision model ────────────────────────────────────────────────
  gemini-3.8-flash: This image shows a solid red square on the left and a solid blue circle on the right against a plain white background.
```

## The code an application copies

**Sending an image.** `Run` takes a string, so build the message yourself and
call `RunMessage`. Normalize the image first:

```go
res, err := imagex.Normalize(data, "") // "" means: detect the format from the bytes
if err != nil { /* unsupported, or cannot fit: tell the user */ }

msg := core.UserMessage{Content: core.Content{
    core.TextBlock{Text: "What is in this picture?"},
    core.ImageBlock{Data: res.Base64(), MimeType: res.MIMEType},
}}
result, err := agent.RunMessage(ctx, msg)
```

**Returning an image from a tool.** Use `Execute` (the `core.ToolResult` form)
and put the image in `Blocks`. Put a short note in `Text`, so the model knows
what it is looking at:

```go
Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
    r := core.OKResult(map[string]any{"captured": true})
    r.Text = "[dashboard snapshot]"
    r.Blocks = []core.ContentBlock{core.ImageBlock{Data: b64, MimeType: "image/png"}}
    return r
},
```

You do not need to resize it. Every image in a tool result is normalized once,
at the history boundary, after `AfterToolCall` runs. That covers built-in
tools, your tools, MCP-bridged tools and images that a hook adds. If an image
cannot be normalized, the original block is **kept** and an
`*agentkit.ImageNormalizationError` is sent to `Hooks.OnError`. The model
still sees an image, and the provider's error, if any, is visible.

**Reading image files.** `read_file` from [`tools`](../../tools) detects
images by magic bytes. It returns a text note (path, format, dimensions, and
whether the image was downscaled) followed by the image. Image files up to
`tools.ImageFileMaxBytes` (32 MiB) are read.

**`imagex` on its own**, for code that handles images outside the loop:

| Function | Does |
|---|---|
| `Sniff(head)` | Detects the format from the first 16 bytes: JPEG, PNG, GIF or WebP. File names are never consulted. |
| `Validate(data, mime)` | Refuses CMYK JPEG, animated PNG and malformed PNG without re-encoding anything. |
| `Normalize(data, mime)` | Fits the image within 2000×2000 and the base64 budget. It works down a fixed JPEG quality ladder (85/70/55/40), then halves the dimensions. An image that already fits is returned **byte for byte**, with `Changed == false`. |
| `FitsBudget(n)` | Whether `n` *decoded* bytes fit once base64-encoded. |
| `DecodeBase64(s)` | A lenient decoder. It accepts newlines, base64url, missing padding and a `data:` URL prefix. |

## Gotchas

- **Images in a user message are not normalized for you.** Only tool results
  pass through the history boundary. Call `imagex.Normalize` before
  `RunMessage`.
- **The model has to declare image input.** Catalog models list
  `"input": ["text", "image"]`. A hand-built `core.Model` must set
  `Input: []string{"text", "image"}`. Without it, every image is replaced by
  `provider.ImagePlaceholder` when the request is built (section 5). The
  history keeps the image, so switching to a vision model later sends it
  again.
- **The budget is measured on base64.** Base64 is 4/3 the size of the bytes
  it encodes. Comparing raw bytes to `MaxBase64Bytes` lets through an image a
  third larger than the limit. Use `FitsBudget`.
- **Decoding is bounded.** `Normalize` reads the header before it allocates
  any pixels. It refuses anything over 16384 px on a side or 40 megapixels,
  so a small PNG that claims to be 30000×30000 cannot exhaust memory.
- **WebP has no encoder.** A WebP that fits is forwarded unchanged. One that
  must shrink is re-encoded as JPEG.
- **Context estimates count an image as a flat cost** (about 1200 tokens), not
  its base64 length. Providers tile and resize on their side. See
  [`compaction`](../compaction).

## See also

- [`imagex`](../../imagex): `Sniff`, `Validate`, `Normalize`, `FitsBudget`,
  `DecodeBase64`
- [`core.ImageBlock`](../../core/message.go),
  [`core.ToolResult.Blocks`](../../core/tool.go)
- [`tools`](../../tools): `read_file`'s image path
- [`images.go`](../../images.go): normalization at the history boundary
