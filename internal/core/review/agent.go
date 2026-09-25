package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type EventKind int

const (
	EventStarted EventKind = iota
	EventTool
	EventDone
	EventFailed
)

type Event struct {
	Agent  string
	Kind   EventKind
	Detail string
}

var errNoSubmission = errors.New("agent finished without calling submit_findings")

// The agent gets the read tools and submit_findings, nothing else.
func (r *Service) runAgent(ctx context.Context, subject *Subject, name, system, prompt string, emit func(Event)) (submission, error) {
	var (
		mu        sync.Mutex
		sub       submission
		submitted bool
	)
	tools := make([]Tool, 0, len(readTools)+1)
	for _, spec := range readTools {
		tools = append(tools, Tool{
			Name: spec.Name, Description: spec.Description, Properties: spec.Properties, Required: spec.Required,
			Call: func(ctx context.Context, input json.RawMessage) (string, error) {
				emit(Event{Agent: name, Kind: EventTool, Detail: toolSummary(spec.Name, input)})
				return subject.exec(ctx, spec.Name, input)
			},
		})
	}
	tools = append(tools, Tool{
		Name: submitTool.Name, Description: submitTool.Description, Properties: submitTool.Properties, Required: submitTool.Required,
		Call: func(_ context.Context, input json.RawMessage) (string, error) {
			var s submission
			if err := json.Unmarshal(input, &s); err != nil {
				return "", fmt.Errorf("invalid submission JSON: %v; call submit_findings again", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if submitted {
				return "", errors.New("findings were already submitted; stop now")
			}
			sub, submitted = s, true
			return "Findings recorded. You are done: stop now.", nil
		},
	})

	err := r.backend.Run(ctx, Task{
		Name: name, System: system, Prompt: prompt, Tools: tools, MaxTurns: r.maxTurns,
		FinishTool: submitTool.Name,
		Done: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return submitted
		},
	})
	mu.Lock()
	defer mu.Unlock()
	if submitted {
		// A backend may still error while winding down after the submission;
		// the findings are what matter.
		return sub, nil
	}
	if err != nil {
		return submission{}, err
	}
	return submission{}, errNoSubmission
}

func toolSummary(name string, input json.RawMessage) string {
	var args map[string]any
	_ = json.Unmarshal(input, &args)
	var parts []string
	for _, k := range []string{"path", "pattern", "ref", "start_line"} {
		if v, ok := args[k]; ok && fmt.Sprint(v) != "" {
			parts = append(parts, fmt.Sprint(v))
		}
	}
	return strings.TrimSpace(name + " " + strings.Join(parts, " "))
}
