package compaction

import (
	"encoding/json"
	"testing"

	"github.com/agentfox/agentkit-go/core"
)

// TS-04-55: The compaction token estimate is the same whether or not a tool
// result carries Metadata.
func TestEstimateUnchangedByMetadata_TS04_55(t *testing.T) {
	call, err := core.NewToolUse("c1", "find_files", json.RawMessage(`{"pattern":"**/*.go"}`))
	if err != nil {
		t.Fatal(err)
	}

	msgs := core.Messages{
		core.UserMessage{Content: core.Content{core.TextBlock{Text: "list the go files"}}},
		core.AssistantMessage{
			Content:    core.Content{core.TextBlock{Text: "Looking."}, call},
			StopReason: core.StopReasonToolUse,
		},
		core.ToolResultMessage{
			ToolUseID: "c1",
			ToolName:  "find_files",
			Content:   core.Content{core.TextBlock{Text: `{"ok":true,"data":{"entries":["main.go"]}}`}},
		},
	}

	two := 2
	msgsMD := msgs.Clone()
	msgsMD[2] = func() core.Message {
		tr := msgsMD[2].(core.ToolResultMessage)
		tr.Metadata = &core.ToolMetadata{
			Truncated:   true,
			TruncatedBy: "bytes",
			TotalBytes:  123456,
			SpillPath:   "/tmp/very/long/path/that/should/not/affect/estimate/at/all.log",
			DurationMS:  42,
			ExitCode:    &two,
			Outcome:     "exit",
			LineEnding:  "lf",
		}
		return tr
	}()

	// Without checkpoint.
	est := EstimateContextTokens(msgs, nil)
	estMD := EstimateContextTokens(msgsMD, nil)
	if est != estMD {
		t.Fatalf("without checkpoint: estimate %d != %d with metadata", est, estMD)
	}

	// With an anchored assistant usage before the tool result.
	var u core.Usage
	u.SetField(core.UsageInputTokens, 100)
	u.SetField(core.UsageOutputTokens, 50)
	u.SetField(core.UsageTotalTokens, 150)

	anchored := msgs.Clone()
	am := anchored[1].(core.AssistantMessage)
	am.Usage = u
	anchored[1] = am

	anchoredMD := msgsMD.Clone()
	amMD := anchoredMD[1].(core.AssistantMessage)
	amMD.Usage = u
	anchoredMD[1] = amMD

	estA := EstimateContextTokens(anchored, nil)
	estAMD := EstimateContextTokens(anchoredMD, nil)
	if estA != estAMD {
		t.Fatalf("with anchor: estimate %d != %d with metadata", estA, estAMD)
	}
}
