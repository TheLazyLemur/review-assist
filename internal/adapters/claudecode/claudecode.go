// Package claudecode is the Claude Code backend: it implements review.Backend
// with the claude CLI, through
// pi-claude.
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
	Token      string        // from `claude setup-token`
	Model      string        // e.g. "sonnet", "opus" or a full model id; empty uses claude's default
	Effort     review.Effort // any but none; empty uses claude's default
	Executable string        // claude binary; empty means "claude" on PATH
	// WorkDir is claude's working directory. Use an empty directory: claude
	// sees nothing there, and the agents read code only through their tools.
	WorkDir string
}

type Backend struct{ cfg Config }

var _ review.Backend = (*Backend)(nil)

func New(cfg Config) *Backend {
	if cfg.Token == "" || cfg.WorkDir == "" || !cfg.Effort.Valid() || cfg.Effort == review.EffortNone {
		panic("claudecode.New: a setup-token, a work dir and a valid effort are required")
	}
	return &Backend{cfg: cfg}
}

func (a *Backend) Run(ctx context.Context, run review.AgentRun) error {
	if run.Done == nil || run.FinishTool == "" || run.MaxTurns < 1 {
		panic(fmt.Sprintf("claudecode.Run: incomplete agent run %q", run.Name))
	}
	sess, err := pi.New(ctx, a.options(run))
	if err != nil {
		return fmt.Errorf("start claude: %w", err)
	}
	defer sess.Close()

	turn, err := sess.Prompt(ctx, run.Prompt)
	if err != nil || run.Done() {
		return err
	}
	if turn.IsError {
		return fmt.Errorf("claude: %s %v", turn.Subtype, turn.Errors)
	}
	turn, err = sess.Prompt(ctx, fmt.Sprintf("You must finish by calling the %s tool. Do not answer in prose.", run.FinishTool))
	if err != nil || run.Done() {
		return err
	}
	if turn.IsError {
		return fmt.Errorf("claude: %s %v", turn.Subtype, turn.Errors)
	}
	return nil
}

// The agent run's tools must be the only tools claude can call.
func (a *Backend) options(run review.AgentRun) pi.Options {
	tools := make([]pi.Tool, 0, len(run.Tools))
	for _, t := range run.Tools {
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
		Effort:               string(a.cfg.Effort),
		MaxTurns:             run.MaxTurns,
		SystemPrompt:         run.System,
		NoTools:              pi.NoToolsAll,
		CustomTools:          tools,
		SettingSources:       []string{""},
		NoSessionPersistence: true,
	}
}
