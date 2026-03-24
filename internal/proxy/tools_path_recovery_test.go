package proxy

import (
	"strings"
	"testing"
)

func TestExtractWorkingDirectoryFromMessages_PwdSystemReminder(t *testing.T) {
	messages := []ChatMessage{
		{
			Role:    "user",
			Content: "<system-reminder>\nUser system info\n\n% pwd\n/mnt/d/code/check_modoles\n\n% ls\ncheck_models.py\n</system-reminder>",
		},
	}

	got := extractWorkingDirectoryFromMessages(messages)
	if got != "/mnt/d/code/check_modoles" {
		t.Fatalf("expected cwd from %% pwd block, got %q", got)
	}
}

func TestInjectToolsIntoMessages_UsesPwdBlockForWorkingDirectory(t *testing.T) {
	tools := []Tool{
		{Type: "function", Function: ToolFunction{Name: "Bash", Description: "Execute shell command", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Read", Description: "Read a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Edit", Description: "Edit a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Write", Description: "Write a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Glob", Description: "Find paths", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Grep", Description: "Search file content", Parameters: map[string]interface{}{"type": "object"}}},
	}
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "<system-reminder>\n% pwd\n/mnt/d/code/check_modoles\n\n% ls\ncheck_models.py\n</system-reminder>",
		},
		{Role: "user", Content: "检查 check_models.py 有没有问题"},
	}

	got := injectToolsIntoMessages(messages, tools, "sonnet-4.6", nil)
	if len(got) == 0 {
		t.Fatal("expected injected messages")
	}
	last := got[len(got)-1].Content
	if !strings.Contains(last, "Working directory: /mnt/d/code/check_modoles") {
		t.Fatalf("expected working directory hint in injected prompt, got: %s", last)
	}
}

func TestBuildSessionChainFollowUp_ReadAbsolutePathRecoveryHint(t *testing.T) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "<system-reminder>\n% pwd\n/mnt/d/code/check_modoles\n\n% ls\ncheck_models.py\n</system-reminder>",
		},
		{Role: "user", Content: "检查 @check_models.py"},
		{
			Role:    "assistant",
			Content: "I'll read the file.",
			ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Read", Arguments: `{"file_path":"check_models.py"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Name: "Read", Content: "Error: file_path must be an absolute path"},
		{
			Role:    "assistant",
			Content: "I'll resolve the path.",
			ToolCalls: []ToolCall{
				{ID: "call_2", Type: "function", Function: ToolCallFunction{Name: "Grep", Arguments: `{"pattern":"\\S","path":"./check_models.py"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call_2", Name: "Grep", Content: "check_models.py"},
	}

	got := buildSessionChainFollowUp(messages, "- Read(file_path: str, offset?: num, limit?: num)\n- Grep(pattern: str, path?: str)\n", "")
	if len(got) != 1 {
		t.Fatalf("expected single follow-up message, got %d", len(got))
	}
	body := got[0].Content
	for _, want := range []string{
		"Working directory: /mnt/d/code/check_modoles",
		"Path recovery hint:",
		"Do NOT use __done__ yet",
		`"/mnt/d/code/check_modoles/check_models.py"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected follow-up to contain %q, got: %s", want, body)
		}
	}
}

func TestBuildSessionChainFollowUp_NoPathRecoveryHintWithoutReadFailure(t *testing.T) {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: "<system-reminder>\n% pwd\n/mnt/d/code/check_modoles\n</system-reminder>",
		},
		{Role: "user", Content: "看看这个文件"},
		{
			Role:    "assistant",
			Content: "Searching.",
			ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Grep", Arguments: `{"pattern":"TODO","path":"check_models.py"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Name: "Grep", Content: "check_models.py:12:TODO"},
	}

	got := buildSessionChainFollowUp(messages, "- Read(file_path: str)\n- Grep(pattern: str)\n", "")
	if strings.Contains(got[0].Content, "Path recovery hint:") {
		t.Fatalf("did not expect path recovery hint without prior Read absolute-path failure, got: %s", got[0].Content)
	}
}
