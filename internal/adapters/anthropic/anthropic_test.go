package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/adapters/anthropic"
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

func TestConversationSpeaksTheAnthropicMessagesWireFormat(t *testing.T) {
	// given
	// ... a server that answers a tool call (after a thinking block), then plain text
	srv := &fakeServer{responses: []string{
		message("tool_use", `{"type":"thinking","thinking":"look","signature":""}`, `{"type":"tool_use","id":"t1","name":"get_diff","input":{"path":"a.go"}}`),
		message("end_turn", `{"type":"text","text":"done"}`),
	}}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	model := anthropic.New(anthropic.Config{BaseURL: hs.URL, APIKey: "sk-test", Model: "fake"})
	conv := model.Start("agent", "be careful", []review.ToolSpec{{Name: "get_diff", Description: "d", Properties: map[string]any{}}})

	// when
	// ... the core sends a prompt, then the tool result
	first, err1 := conv.Send(context.Background(), review.Input{Text: []string{"review this"}})
	_, err2 := conv.Send(context.Background(), review.Input{Results: []review.ToolResult{{CallID: "t1", Content: "R2 +y := x / 0"}}})

	// then
	// ... the tool call comes back to the core in port types
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if len(first.Calls) != 1 || first.Calls[0].ID != "t1" || first.Calls[0].Name != "get_diff" || string(first.Calls[0].Input) != `{"path":"a.go"}` {
		t.Fatalf("unexpected calls %+v", first.Calls)
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
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_diff" || req.Thinking.Type != "disabled" {
		t.Errorf("want tool get_diff and thinking disabled, got %+v", req)
	}

	// ... and the second request replays the history with the assistant turn and the tool_result
	if len(req.Messages) != 3 || req.Messages[1].Role != "assistant" || req.Messages[2].Content[0].Type != "tool_result" || req.Messages[2].Content[0].ToolUseID != "t1" {
		t.Errorf("unexpected history %+v", req.Messages)
	}
}
