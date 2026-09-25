// Package anthropic implements review.Model over the Anthropic Messages API
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
	// LogPath, when set, receives one line per model call (timing, tokens).
	LogPath string
}

type Model struct {
	cfg    Config
	client sdk.Client
}

var _ review.Model = (*Model)(nil)

func New(cfg Config) *Model {
	if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" {
		panic(fmt.Sprintf("anthropic.New: base URL, API key and model are required (url=%q model=%q key set=%v)",
			cfg.BaseURL, cfg.Model, cfg.APIKey != ""))
	}
	return &Model{cfg: cfg, client: sdk.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(2),
	)}
}

func (m *Model) Start(label, system string, tools []review.ToolSpec) review.Conversation {
	params := make([]sdk.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		params = append(params, sdk.ToolUnionParam{OfTool: &sdk.ToolParam{
			Name:        t.Name,
			Description: sdk.String(t.Description),
			InputSchema: sdk.ToolInputSchemaParam{Properties: t.Properties, Required: t.Required},
		}})
	}
	return &conversation{m: m, label: label, system: system, tools: params}
}

type conversation struct {
	m        *Model
	label    string
	system   string
	tools    []sdk.ToolUnionParam
	messages []sdk.MessageParam
	turn     int
}

func (c *conversation) Send(ctx context.Context, in review.Input) (review.Reply, error) {
	var blocks []sdk.ContentBlockParamUnion
	for _, r := range in.Results {
		blocks = append(blocks, sdk.NewToolResultBlock(r.CallID, r.Content, r.IsError))
	}
	for _, t := range in.Text {
		blocks = append(blocks, sdk.NewTextBlock(t))
	}
	if len(blocks) == 0 {
		return review.Reply{}, fmt.Errorf("anthropic: empty user turn")
	}
	c.messages = append(c.messages, sdk.NewUserMessage(blocks...))

	params := sdk.MessageNewParams{
		Model:     sdk.Model(c.m.cfg.Model),
		MaxTokens: 8192,
		System:    []sdk.TextBlockParam{{Text: c.system}},
		Messages:  c.messages,
		Tools:     c.tools,
	}
	if !c.m.cfg.Think {
		params.Thinking = sdk.ThinkingConfigParamUnion{OfDisabled: &sdk.ThinkingConfigDisabledParam{}}
	}
	start := time.Now()
	resp, err := c.m.client.Messages.New(ctx, params)
	c.m.log(c.label, c.turn, start, resp, err)
	c.turn++
	if err != nil {
		return review.Reply{}, err
	}
	// Keep the full reply (thinking blocks included) so the history stays valid.
	c.messages = append(c.messages, resp.ToParam())

	reply := review.Reply{Truncated: resp.StopReason == sdk.StopReasonMaxTokens}
	for _, b := range resp.Content {
		if tu, ok := b.AsAny().(sdk.ToolUseBlock); ok {
			reply.Calls = append(reply.Calls, review.ToolCall{ID: tu.ID, Name: tu.Name, Input: tu.Input})
		}
	}
	return reply, nil
}

func (m *Model) log(agent string, turn int, start time.Time, resp *sdk.Message, err error) {
	if m.cfg.LogPath == "" {
		return
	}
	f, ferr := os.OpenFile(m.cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
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
