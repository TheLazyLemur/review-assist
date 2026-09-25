package claudecode

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	pi "github.com/TheLazyLemur/pi-claude"

	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

func TestSessionIsBareWithOnlyTheTaskTools(t *testing.T) {
	// given
	// ... a Claude Code agent with a setup-token, and a task with a read tool and a no-argument tool
	a := New(Config{Token: "sk-ant-oat-test", Model: "sonnet", Effort: "low", WorkDir: "/tmp/empty"})
	task := review.Task{
		Name: "L1", System: "be careful", Prompt: "review", MaxTurns: 12, FinishTool: "submit_findings",
		Done: func() bool { return false },
		Tools: []review.Tool{{
			Name: "grep", Description: "search", Properties: map[string]any{"pattern": map[string]any{"type": "string"}}, Required: []string{"pattern"},
			Call: func(_ context.Context, in json.RawMessage) (string, error) { return "hit: " + string(in), nil },
		}, {
			Name: "list_changed_files", Description: "list", Properties: map[string]any{},
			Call: func(context.Context, json.RawMessage) (string, error) { return "", nil },
		}},
	}

	// when
	// ... the session options are built
	opts := a.options(task)

	// then
	// ... claude runs bare on the token, blind to inherited Anthropic settings
	for _, want := range []string{"CLAUDE_CODE_SIMPLE=1", "ANTHROPIC_AUTH_TOKEN=sk-ant-oat-test", "ANTHROPIC_API_KEY=", "ANTHROPIC_BASE_URL=", "ANTHROPIC_MODEL="} {
		if !slices.Contains(opts.Env, want) {
			t.Errorf("env %v is missing %q", opts.Env, want)
		}
	}

	// ... with no built-in tools, settings, MCP servers or saved session: only the task's tools
	if opts.NoTools != pi.NoToolsAll || !slices.Equal(opts.SettingSources, []string{""}) || !opts.NoSessionPersistence {
		t.Errorf("session is not locked down: %+v", opts)
	}
	if len(opts.CustomTools) != 2 || opts.CustomTools[0].Name != "grep" {
		t.Fatalf("want only the task's two tools, got %+v", opts.CustomTools)
	}

	// ... each schema is valid JSON Schema: claude drops every tool if one has "required": null
	schema, _ := json.Marshal(opts.CustomTools[1].Schema)
	if string(schema) != `{"properties":{},"type":"object"}` {
		t.Errorf("no-argument tool schema: %s", schema)
	}
	if opts.SystemPrompt != "be careful" || opts.Model != "sonnet" || opts.Effort != "low" || opts.MaxTurns != 12 || opts.CWD != "/tmp/empty" {
		t.Errorf("task settings not passed through: %+v", opts)
	}

	// ... and the tool runs the task's handler with the model's input
	res, err := opts.CustomTools[0].Execute(context.Background(), json.RawMessage(`{"pattern":"x"}`))
	if err != nil || res.IsError || res.Text != `hit: {"pattern":"x"}` {
		t.Errorf("tool call: %+v %v", res, err)
	}
}
