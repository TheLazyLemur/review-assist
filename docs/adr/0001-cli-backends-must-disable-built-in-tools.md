# CLI backends must disable their built-in tools

Agents must stay read-only: they suggest, and only the user posts. A CLI
backend such as Claude Code comes with its own tools (shell, file edits, web,
MCP servers), and any one of them could write files, run commands or reach the
code host. So a command-line agent qualifies as a backend only if its built-in
tools can be turned off and review-assist's tools plugged in; then the task's
tools are all it can call. We keep the read-only guarantee in the tools rather
than in instructions to the model, and accept that some CLI agents cannot be
supported.

## Considered Options

- **Tell the agent not to write.** Rejected: a prompt is not a control, and a
  model that ignores it acts with the user's credentials.
- **Run the agent in a sandbox** (container, read-only mount, no network).
  Rejected for now: it depends on the operating system, needs network access
  to the model anyway, and still leaves the agent's own tools able to reach
  the code host through that network.
