// Package claudecode implements review.Agent with the claude CLI (Claude
// Code), driven through pi-claude. Claude runs its own agent loop and calls
// the task's tools, which are the only tools it gets.
//
// claude runs in bare mode (CLAUDE_CODE_SIMPLE=1, what --bare sets) and logs
// in with a `claude setup-token` token passed as ANTHROPIC_AUTH_TOKEN. Bare
// mode never reads the keychain. Taking the token as ANTHROPIC_AUTH_TOKEN is
// undocumented: if reviews start failing with "Not logged in" or hang after a
// claude upgrade, suspect this first. claude-code-wire-proxy relies on the
// same behaviour.
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"

	pi "github.com/TheLazyLemur/pi-claude"

	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

type Config struct {
	Token      string // from `claude setup-token`
	Model      string // e.g. "sonnet", "opus" or a full model id; empty uses claude's default
	Effort     string // low, medium, high, xhigh or max; empty uses claude's default
	Executable string // claude binary; empty means "claude" on PATH
	// WorkDir is claude's working directory. Use an empty directory: claude
	// sees nothing there, and the agents read code only through their tools.
	WorkDir string
}

type Agent struct{ cfg Config }

var _ review.Agent = (*Agent)(nil)

func New(cfg Config) *Agent {
	if cfg.Token == "" || cfg.WorkDir == "" {
		panic("claudecode.New: a setup-token and a work dir are required")
	}
	return &Agent{cfg: cfg}
}

func (a *Agent) Run(ctx context.Context, task review.Task) error {
	if task.Done == nil || task.FinishTool == "" || task.MaxTurns < 1 {
		panic(fmt.Sprintf("claudecode.Run: incomplete task %q", task.Name))
	}
	sess, err := pi.New(ctx, a.options(task))
	if err != nil {
		return fmt.Errorf("start claude: %w", err)
	}
	defer sess.Close()

	turn, err := sess.Prompt(ctx, task.Prompt)
	if err != nil || task.Done() {
		return err
	}
	if turn.IsError {
		return fmt.Errorf("claude: %s %v", turn.Subtype, turn.Errors)
	}
	// claude ended its turn without finishing. Remind once.
	turn, err = sess.Prompt(ctx, fmt.Sprintf("You must finish by calling the %s tool. Do not answer in prose.", task.FinishTool))
	if err != nil || task.Done() {
		return err
	}
	if turn.IsError {
		return fmt.Errorf("claude: %s %v", turn.Subtype, turn.Errors)
	}
	return nil
}

// options locks the session down: bare mode on the token, inherited Anthropic
// settings blanked, no built-in tools, no settings files, no MCP servers, no
// saved session. The task's tools are all claude can call.
func (a *Agent) options(task review.Task) pi.Options {
	tools := make([]pi.Tool, 0, len(task.Tools))
	for _, t := range task.Tools {
		call := t.Call
		schema := map[string]any{"type": "object", "properties": t.Properties}
		if len(t.Required) > 0 {
			// "required": null is invalid, and claude then drops every tool.
			schema["required"] = t.Required
		}
		tools = append(tools, pi.Tool{
			Name:        t.Name,
			Description: t.Description,
			Schema:      schema,
			Execute: func(ctx context.Context, args json.RawMessage) (pi.ToolResult, error) {
				out, err := call(ctx, args)
				if err != nil {
					return pi.Errorf("error: %v", err), nil
				}
				return pi.Text("%s", out), nil
			},
		})
	}
	return pi.Options{
		CWD: a.cfg.WorkDir,
		Env: []string{
			"CLAUDE_CODE_SIMPLE=1",
			"ANTHROPIC_AUTH_TOKEN=" + a.cfg.Token,
			// Blank what the user's shell may export for other tools, so the
			// token above is the only login and the model is ours.
			"ANTHROPIC_API_KEY=",
			"ANTHROPIC_BASE_URL=",
			"ANTHROPIC_MODEL=",
		},
		Executable:           a.cfg.Executable,
		Model:                a.cfg.Model,
		Effort:               a.cfg.Effort,
		MaxTurns:             task.MaxTurns,
		SystemPrompt:         task.System,
		NoTools:              pi.NoToolsAll,
		CustomTools:          tools,
		SettingSources:       []string{""},
		NoSessionPersistence: true,
	}
}
