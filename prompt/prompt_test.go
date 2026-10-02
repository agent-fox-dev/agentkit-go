package prompt

import (
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-02-55: The prompt builder emits the execute fallback only when no navigation tool is present
func TestExecuteFallbackSuppression_TS02_55(t *testing.T) {
	// Helper to build a minimal tool set.
	mkTool := func(name string, guidelines ...string) core.Tool {
		return core.Tool{Name: name, PromptGuidelines: guidelines}
	}

	tests := []struct {
		name         string
		tools        []core.Tool
		wantFallback bool
	}{
		{
			name:         "execute alone",
			tools:        []core.Tool{mkTool("execute")},
			wantFallback: true,
		},
		{
			name:         "execute with file_outline only",
			tools:        []core.Tool{mkTool("execute"), mkTool("file_outline")},
			wantFallback: false,
		},
		{
			name:         "execute with find_symbol only",
			tools:        []core.Tool{mkTool("execute"), mkTool("find_symbol")},
			wantFallback: false,
		},
		{
			name:         "execute with list_files",
			tools:        []core.Tool{mkTool("execute"), mkTool("list_files")},
			wantFallback: false,
		},
		{
			name:         "execute with all navigation tools",
			tools:        []core.Tool{mkTool("execute"), mkTool("list_files"), mkTool("find_files"), mkTool("search_files"), mkTool("file_outline"), mkTool("find_symbol")},
			wantFallback: false,
		},
		{
			name:         "no execute at all",
			tools:        []core.Tool{mkTool("read_file")},
			wantFallback: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Build(Input{Tools: tt.tools})
			hasFallback := contains(got, tools.ExecuteFallbackGuideline)
			if hasFallback != tt.wantFallback {
				t.Errorf("ExecuteFallbackGuideline present=%v, want %v\nprompt:\n%s", hasFallback, tt.wantFallback, got)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
