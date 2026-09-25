// Package anthropic implements review.Agent over the Anthropic Messages API
// (<base URL>/v1/messages) with the official SDK. Any compatible endpoint
// works: Ollama, Anthropic, or a proxy.
package anthropic

import (
	"context"
	"fmt"
	"os"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

type Config struct {
	BaseURL string // e.g. http://localhost:11434 (Ollama) or https://api.anthropic.com
	APIKey  string // sent as x-api-key; Ollama ignores it
	Model   string
	// Think leaves the model's extended thinking on. Off by default: agents
	// reason through their tool calls, and thinking multiplies turn latency.
	Think bool
	// LogPath gets one line per model call (timing, tokens).
	LogPath string
}

type Agent struct {
	cfg    Config
	client sdk.Client
}

var _ review.Backend = (*Agent)(nil)

func New(cfg Config) *Agent {
	if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" {
		panic(fmt.Sprintf("anthropic.New: base URL, API key and model are required (url=%q model=%q key set=%v)",
			cfg.BaseURL, cfg.Model, cfg.APIKey != ""))
	}
	return &Agent{cfg: cfg, client: sdk.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(2),
	)}
}

func (a *Agent) Run(ctx context.Context, task review.Task) error {
	if task.Done == nil || task.FinishTool == "" || task.MaxTurns < 4 {
		panic(fmt.Sprintf("anthropic.Run: incomplete task %q", task.Name))
	}
	tools := make([]sdk.ToolUnionParam, 0, len(task.Tools))
	byName := make(map[string]review.Tool, len(task.Tools))
	for _, t := range task.Tools {
		byName[t.Name] = t
		tools = append(tools, sdk.ToolUnionParam{OfTool: &sdk.ToolParam{
			Name:        t.Name,
			Description: sdk.String(t.Description),
			InputSchema: sdk.ToolInputSchemaParam{Properties: t.Properties, Required: t.Required},
		}})
	}
	nudge := func(text string) sdk.ContentBlockParamUnion { return sdk.NewTextBlock(text) }
	next := []sdk.ContentBlockParamUnion{sdk.NewTextBlock(task.Prompt)}
	var messages []sdk.MessageParam
	nudged := false

	for turn := 0; turn < task.MaxTurns; turn++ {
		if turn == task.MaxTurns-3 {
			next = append(next, nudge(fmt.Sprintf("You are nearly out of turns. Call %s now with what you have verified.", task.FinishTool)))
		}
		messages = append(messages, sdk.NewUserMessage(next...))
		params := sdk.MessageNewParams{
			Model:     sdk.Model(a.cfg.Model),
			MaxTokens: 8192,
			System:    []sdk.TextBlockParam{{Text: task.System}},
			Messages:  messages,
			Tools:     tools,
		}
		if !a.cfg.Think {
			params.Thinking = sdk.ThinkingConfigParamUnion{OfDisabled: &sdk.ThinkingConfigDisabledParam{}}
		}
		start := time.Now()
		resp, err := a.client.Messages.New(ctx, params)
		a.log(task.Name, turn, start, resp, err)
		if err != nil {
			return fmt.Errorf("model call: %w", err)
		}
		// Keep the full reply (thinking blocks included) so the history stays valid.
		messages = append(messages, resp.ToParam())

		next = nil
		for _, b := range resp.Content {
			tu, ok := b.AsAny().(sdk.ToolUseBlock)
			if !ok {
				continue
			}
			tool, ok := byName[tu.Name]
			if !ok {
				next = append(next, sdk.NewToolResultBlock(tu.ID, fmt.Sprintf("error: unknown tool %q", tu.Name), true))
				continue
			}
			out, err := tool.Call(ctx, tu.Input)
			if err != nil {
				next = append(next, sdk.NewToolResultBlock(tu.ID, "error: "+err.Error(), true))
				continue
			}
			next = append(next, sdk.NewToolResultBlock(tu.ID, out, false))
		}
		if task.Done() {
			return nil
		}

		if len(next) > 0 {
			continue
		}
		if resp.StopReason == sdk.StopReasonMaxTokens {
			next = []sdk.ContentBlockParamUnion{nudge(fmt.Sprintf("Your reply was cut off. Be brief and call %s.", task.FinishTool))}
			continue
		}
		// The model stopped talking without a tool call. Small models do this;
		// remind once, then give up.
		if nudged {
			return nil
		}
		nudged = true
		next = []sdk.ContentBlockParamUnion{nudge(fmt.Sprintf("You must finish by calling the %s tool. Do not answer in prose.", task.FinishTool))}
	}
	return fmt.Errorf("agent used all %d turns", task.MaxTurns)
}

func (a *Agent) log(agent string, turn int, start time.Time, resp *sdk.Message, err error) {
	if a.cfg.LogPath == "" {
		return
	}
	f, ferr := os.OpenFile(a.cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	line := fmt.Sprintf("%s %q turn=%d took=%s", time.Now().Format(time.RFC3339), agent, turn, time.Since(start).Round(time.Millisecond))
	if err != nil {
		line += " err=" + err.Error()
	} else {
		var calls []string
		for _, b := range resp.Content {
			if tu, ok := b.AsAny().(sdk.ToolUseBlock); ok {
				calls = append(calls, tu.Name)
			}
		}
		line += fmt.Sprintf(" in=%d out=%d stop=%s tools=%v", resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.StopReason, calls)
	}
	fmt.Fprintln(f, line)
}
