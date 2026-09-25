package messagesapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/adapters/messagesapi"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

// fakeServer replays scripted Anthropic Messages responses and records requests.
type fakeServer struct {
	mu        sync.Mutex
	responses []string
	requests  []string
	apiKeys   []string
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.requests = append(f.requests, string(body))
	f.apiKeys = append(f.apiKeys, r.Header.Get("x-api-key"))
	if r.URL.Path != "/v1/messages" || len(f.responses) == 0 {
		http.Error(w, "unexpected request "+r.URL.Path, http.StatusBadRequest)
		return
	}
	w.Header().Set("content-type", "application/json")
	_, _ = io.WriteString(w, f.responses[0])
	f.responses = f.responses[1:]
}

func message(stop string, content ...string) string {
	return `{"id":"m","type":"message","role":"assistant","model":"fake","stop_reason":"` + stop +
		`","usage":{"input_tokens":1,"output_tokens":1},"content":[` + strings.Join(content, ",") + `]}`
}

func TestAgentLoopsOverTheMessagesAPIUntilTheTaskIsDone(t *testing.T) {
	// given
	// ... a server that answers with a tool call (after a thinking block), then the finishing call
	srv := &fakeServer{responses: []string{
		message("tool_use", `{"type":"thinking","thinking":"look","signature":""}`, `{"type":"tool_use","id":"t1","name":"get_diff","input":{"path":"a.go"}}`),
		message("tool_use", `{"type":"tool_use","id":"t2","name":"finish","input":{}}`),
	}}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	agent := messagesapi.New(messagesapi.Config{BaseURL: hs.URL, APIKey: "sk-test", Model: "fake"})
	var gotInput string
	done := false
	task := review.Task{
		Name: "agent", System: "be careful", Prompt: "review this", MaxTurns: 10, FinishTool: "finish",
		Done: func() bool { return done },
		Tools: []review.Tool{
			{Name: "get_diff", Description: "d", Properties: map[string]any{},
				Call: func(_ context.Context, in json.RawMessage) (string, error) {
					gotInput = string(in)
					return "R2 +y := x / 0", nil
				}},
			{Name: "finish", Description: "f", Properties: map[string]any{},
				Call: func(context.Context, json.RawMessage) (string, error) { done = true; return "ok", nil }},
		},
	}

	// when
	// ... the agent runs the task
	err := agent.Run(context.Background(), task)

	// then
	// ... it called the tool with the model's input and stopped once the task was done
	if err != nil {
		t.Fatal(err)
	}
	if gotInput != `{"path":"a.go"}` || !done || len(srv.requests) != 2 {
		t.Fatalf("input %q done %v requests %d", gotInput, done, len(srv.requests))
	}

	// ... every request carried the key, the tools, and disabled thinking
	var req struct {
		Tools    []struct{ Name string }
		Thinking struct{ Type string }
		Messages []struct {
			Role    string
			Content []struct {
				Type      string
				ToolUseID string `json:"tool_use_id"`
				Content   any
			}
		}
	}
	for i, k := range srv.apiKeys {
		if k != "sk-test" {
			t.Errorf("request %d: want x-api-key sk-test, got %q", i, k)
		}
	}
	if err := json.Unmarshal([]byte(srv.requests[1]), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 2 || req.Tools[0].Name != "get_diff" || req.Thinking.Type != "disabled" {
		t.Errorf("want both tools and thinking disabled, got %+v", req)
	}

	// ... and the second request replays the history with the assistant turn and the tool_result
	if len(req.Messages) != 3 || req.Messages[1].Role != "assistant" || req.Messages[2].Content[0].Type != "tool_result" || req.Messages[2].Content[0].ToolUseID != "t1" {
		t.Errorf("unexpected history %+v", req.Messages)
	}
}

func TestEffortTurnsOnAdaptiveThinkingAtThatEffort(t *testing.T) {
	// given
	// ... a backend set to high effort, and a server that finishes at once
	srv := &fakeServer{responses: []string{message("tool_use", `{"type":"tool_use","id":"t1","name":"finish","input":{}}`)}}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	backend := messagesapi.New(messagesapi.Config{BaseURL: hs.URL, APIKey: "k", Model: "fake", Effort: review.EffortHigh})
	done := false

	// when
	// ... it runs a task
	err := backend.Run(context.Background(), review.Task{
		Name: "a", Prompt: "p", MaxTurns: 4, FinishTool: "finish", Done: func() bool { return done },
		Tools: []review.Tool{{Name: "finish", Properties: map[string]any{},
			Call: func(context.Context, json.RawMessage) (string, error) { done = true; return "ok", nil }}},
	})

	// then
	// ... the request asks for adaptive thinking at high effort
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Thinking     struct{ Type string }
		OutputConfig struct{ Effort string } `json:"output_config"`
	}
	if err := json.Unmarshal([]byte(srv.requests[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req.Thinking.Type != "adaptive" || req.OutputConfig.Effort != "high" {
		t.Errorf("want adaptive thinking at high effort, got %+v", req)
	}
}
