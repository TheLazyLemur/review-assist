package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// EventKind describes agent progress for the UI.
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

// runAgent drives one agent until it calls submit_findings. It only ever
// executes the workspace's read tools.
func (r *Reviewer) runAgent(ctx context.Context, ws *Workspace, name, system, prompt string, emit func(Event)) (submission, error) {
	conv := r.model.Start(name, system, append(append([]ToolSpec{}, readTools...), submitTool))
	in := Input{Text: []string{prompt}}
	nudged := false

	for turn := 0; turn < r.maxTurns; turn++ {
		if turn == r.maxTurns-3 {
			in.Text = append(in.Text, "You are nearly out of turns. Call submit_findings now with what you have verified.")
		}
		reply, err := conv.Send(ctx, in)
		if err != nil {
			return submission{}, fmt.Errorf("model call: %w", err)
		}

		in = Input{}
		for _, call := range reply.Calls {
			if call.Name == submitTool.Name {
				var sub submission
				if err := json.Unmarshal(call.Input, &sub); err != nil {
					in.Results = append(in.Results, ToolResult{CallID: call.ID, Content: "invalid submission JSON: " + err.Error() + "; call submit_findings again", IsError: true})
					continue
				}
				return sub, nil
			}
			emit(Event{Agent: name, Kind: EventTool, Detail: toolSummary(call.Name, call.Input)})
			out, err := ws.exec(ctx, call.Name, call.Input)
			if err != nil {
				in.Results = append(in.Results, ToolResult{CallID: call.ID, Content: "error: " + err.Error(), IsError: true})
				continue
			}
			in.Results = append(in.Results, ToolResult{CallID: call.ID, Content: out})
		}

		if len(in.Results) > 0 {
			continue
		}
		if reply.Truncated {
			in.Text = []string{"Your reply was cut off. Be brief and call submit_findings."}
			continue
		}
		// The model stopped talking without a tool call. Small models do this;
		// remind once, then give up.
		if nudged {
			return submission{}, errNoSubmission
		}
		nudged = true
		in.Text = []string{"You must finish by calling the submit_findings tool (an empty list is fine). Do not answer in prose."}
	}
	return submission{}, fmt.Errorf("agent used all %d turns without submitting", r.maxTurns)
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
