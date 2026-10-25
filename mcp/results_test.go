package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// image is a block whose base64 encoding is n characters.
func image(n int) *sdk.ImageContent {
	return &sdk.ImageContent{Data: make([]byte, n/4*3), MIMEType: "image/png"}
}

func texts(n, size int) []Content {
	out := make([]Content, n)
	for i := range out {
		out[i] = &TextContent{Text: strings.Repeat("漢", size)}
	}
	return out
}

var droppedNote = regexp.MustCompile(`^\n\n\[truncated: .* (\d+) further content block\(s\) were dropped\. Narrow the request`)

// shape summarises capped content: "text:<runes>", "image", "audio", "link",
// and "note:<dropped>" for the truncation note.
func shape(items []Content) []string {
	var out []string
	for _, it := range items {
		switch c := it.(type) {
		case *TextContent:
			if m := droppedNote.FindStringSubmatch(c.Text); m != nil {
				out = append(out, "note:"+m[1])
				continue
			}
			out = append(out, fmt.Sprintf("text:%d", len([]rune(c.Text))))
		case *sdk.ImageContent:
			out = append(out, "image")
		case *sdk.AudioContent:
			out = append(out, "audio")
		case *sdk.ResourceLink:
			out = append(out, "link")
		}
	}
	return out
}

// The 50K-character budget is spent in runes across the whole result; a text
// block is cut at the boundary, a non-text block is charged its encoded size
// and kept or dropped whole (half an image is a corrupt image), and a note
// tells the model how many blocks it did not see.
func TestCapContentSpendsOneBudgetAcrossTheResult(t *testing.T) {
	link := &sdk.ResourceLink{URI: "file:///x", Name: strings.Repeat("n", 100)}
	audio := &sdk.AudioContent{Data: make([]byte, 30_000), MIMEType: "audio/wav"}
	for _, tc := range []struct {
		name string
		in   []Content
		want []string
	}{
		{"under the cap in runes", texts(1, ResultCharCap-10), []string{"text:49990"}},
		{"exactly the cap", texts(2, ResultCharCap/2), []string{"text:25000", "text:25000"}},
		{"cut across blocks", texts(200, 49_000), []string{"text:49000", "text:1000", "note:198"}},
		{"cut at a block boundary", texts(3, ResultCharCap/2), []string{"text:25000", "text:25000", "note:0"}},
		{"an image that fits spends the budget", append([]Content{image(40_000)}, texts(1, 20_000)...),
			[]string{"image", "text:10000", "note:0"}},
		{"an image that does not fit is dropped whole", append(texts(1, 20_000), image(40_000), &TextContent{Text: "after"}),
			[]string{"text:20000", "note:2"}},
		{"audio is charged its encoded size", append([]Content{audio}, texts(1, 20_000)...),
			[]string{"audio", "text:10000", "note:0"}},
		{"other blocks are charged their JSON", append([]Content{link}, texts(1, ResultCharCap)...),
			[]string{"link", fmt.Sprintf("text:%d", ResultCharCap-len(mustMarshal(t, link))), "note:0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shape(CapContent(tc.in)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("capped to %v, want %v", got, tc.want)
			}
		})
	}
}

func mustMarshal(t *testing.T, c Content) []byte {
	b, err := c.MarshalJSON()
	must(t, err)
	return b
}

func TestACallsResultIsCapped(t *testing.T) {
	s := newServer("big")
	handle(s, &Tool{Name: "big"}, func(map[string]any) (*CallToolResult, error) {
		return &CallToolResult{Content: texts(3, ResultCharCap/2)}, nil
	})
	conn, _ := pair(t, s, ServerConfig{Name: "big"}, ConnectionOptions{})
	res, err := conn.Call(context.Background(), "big", nil)
	must(t, err)
	if got := shape(res.Content); !reflect.DeepEqual(got, []string{"text:25000", "text:25000", "note:0"}) {
		t.Fatalf("content = %v, want the cap applied", got)
	}
}

// An object structuredContent is Data itself, any other value sits under
// "value", and with none Data is the joined text and the raw blocks; Text is
// always the text blocks joined in order, what the model reads.
func TestAResultsDataAndTextComeFromItsContent(t *testing.T) {
	img := &sdk.ImageContent{Data: []byte{0x89, 'P', 'N', 'G'}, MIMEType: "image/png"}
	two := []Content{&TextContent{Text: "hello "}, img, &TextContent{Text: "wor\nld"}}
	for _, tc := range []struct {
		name       string
		structured any
		want       map[string]any
	}{
		{"an object is Data", map[string]any{"temperature": 72.5, "units": "F"},
			map[string]any{"temperature": 72.5, "units": "F"}},
		{"a number is wrapped", 42, map[string]any{"value": float64(42)}},
		{"a string is wrapped", "done", map[string]any{"value": "done"}},
		{"an array is wrapped", []any{"a", 1}, map[string]any{"value": []any{"a", float64(1)}}},
		{"none is text and content", nil, map[string]any{"text": "hello wor\nld", "content": []json.RawMessage{
			mustMarshal(t, two[0]), mustMarshal(t, img), mustMarshal(t, two[2])}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer("r")
			handle(s, &Tool{Name: "r"}, func(map[string]any) (*CallToolResult, error) {
				return &CallToolResult{Content: two, StructuredContent: tc.structured}, nil
			})
			res := toolsOf(t, poolOf(t, s, ServerConfig{Name: "srv"}))["srv__r"].Execute(context.Background(), nil)
			if !res.OK || res.Text != "hello wor\nld" {
				t.Fatalf("result = %+v, want OK with the joined text", res)
			}
			if !reflect.DeepEqual(res.Data, tc.want) {
				t.Fatalf("Data = %#v, want %#v", res.Data, tc.want)
			}
		})
	}
}
