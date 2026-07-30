package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// The manual is what an agent reads before its first call, so it drifting out
// of step with the real tool set would mislead every caller. These two tests
// pin it to the code.

func TestUsageMentionsEveryTool(t *testing.T) {
	b, err := json.Marshal(toolsList())
	if err != nil {
		t.Fatalf("marshal toolsList: %v", err)
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &listed); err != nil {
		t.Fatalf("unmarshal toolsList: %v", err)
	}
	if len(listed.Tools) == 0 {
		t.Fatal("toolsList advertises nothing")
	}
	for _, tl := range listed.Tools {
		if !strings.Contains(usageMarkdown, "`"+tl.Name+"`") {
			t.Errorf("usage.md never mentions the tool `%s`", tl.Name)
		}
	}
}

func TestUsageDocumentsErrorCodes(t *testing.T) {
	// Every code errorResult can emit; see its doc comment.
	for _, code := range []string{"invalid_input", "network_error", "rate_limited", "workspace_error"} {
		if !strings.Contains(usageMarkdown, code) {
			t.Errorf("usage.md never documents the error code %q", code)
		}
	}
}

// Every argument the tools accept must appear in the manual, or a caller reading
// only the manual cannot use them.
func TestUsageDocumentsEveryArgument(t *testing.T) {
	b, err := json.Marshal(toolsList())
	if err != nil {
		t.Fatalf("marshal toolsList: %v", err)
	}
	var listed struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &listed); err != nil {
		t.Fatalf("unmarshal toolsList: %v", err)
	}
	for _, tl := range listed.Tools {
		for arg := range tl.InputSchema.Properties {
			if !strings.Contains(usageMarkdown, "`"+arg+"`") {
				t.Errorf("usage.md never documents %s's %q argument", tl.Name, arg)
			}
		}
	}
}

// The instructions are the only text some clients surface, so they must carry
// the two things a caller most needs to know.
func TestInstructionsCoverTheEssentials(t *testing.T) {
	for _, want := range []string{"get_usage", "truncated", "not PTR", "ip.thc.org"} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("Instructions should mention %q", want)
		}
	}
}

// The manual must warn that matching_records of 0 means unknown, since reading
// it as "none" would silently understate every large block.
func TestUsageWarnsAboutUnknownTotal(t *testing.T) {
	if !strings.Contains(usageMarkdown, "0 means unknown") {
		t.Error("usage.md should warn that matching_records of 0 means unknown, not none")
	}
}
