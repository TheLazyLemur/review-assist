package review

// One lens per specialist, so each agent checks one kind of problem.

type LensID string

const (
	L1 LensID = "L1"
	L2 LensID = "L2"
	L3 LensID = "L3"
	L4 LensID = "L4"
	L5 LensID = "L5"
	L6 LensID = "L6"
	L7 LensID = "L7"
	L8 LensID = "L8"
	L9 LensID = "L9"
)

type Lens struct {
	ID        LensID
	Name      string
	Checklist string
}

var lenses = map[LensID]Lens{
	L1: {L1, "Correctness & Logic", `- Does the algorithm produce the right output for all inputs, not just the happy path?
- Off-by-one, inverted conditionals, wrong operator precedence, wrong boolean logic.
- Edge & boundary cases: empty collection, nil/null, zero, negative, max value, single vs many, unicode / empty string.
- State & side effects: functions mutating inputs unexpectedly, hidden global state, ordering dependencies between calls.
- Determinism: same input -> same output where it should.
- Does the diff actually do what the PR title/description says?`},
	L2: {L2, "Failure & Robustness", `- Errors caught, propagated, or silently swallowed?
- A defensive fallback that silently masks a violated invariant (instead of failing loud) is a finding.
- Cleanup on the failure path: closing files, releasing locks, rolling back.
- Behaviour when a downstream call times out or returns garbage.
- Resource leaks: unclosed handles/connections, unbounded caches/queues, goroutines/threads that never terminate, listeners never removed.`},
	L3: {L3, "Concurrency & Data Integrity", `- Shared mutable state read/written without synchronisation.
- Check-then-act races (TOCTOU); non-atomic read-modify-write on shared state.
- Lock ordering that can deadlock against another path.
- Transaction boundaries: is the unit of work actually atomic? partial-write risk?
- Idempotency: safe to retry after a partial failure or timeout?
- Migration / back-compat: can old and new code run simultaneously mid-deploy?`},
	L4: {L4, "Security", `- Injection: SQL, command, template, path traversal.
- Unvalidated / untrusted input crossing a trust boundary.
- Missing authorisation checks.
- Secrets hardcoded or written to logs.
- Unsafe deserialisation; SSRF.
- Non-constant-time comparison for tokens/secrets.`},
	L5: {L5, "Performance & Complexity", `- Accidental O(n^2) from nested loops; N+1 query patterns; queries missing indexes.
- Work inside a loop that could be hoisted out.
- Unnecessary allocations in hot paths.
- Data structure mismatched to the access pattern.`},
	L6: {L6, "API & Compatibility", `- Signature / return / error contracts clear and hard to misuse?
- Backward compatibility for existing callers (read the callers).
- Language/framework idioms; ignored compiler/linter warnings.
- Correct async handling; integer overflow; float comparison.`},
	L7: {L7, "Tests & Verifiability", `- Do tests cover the edge cases and failure paths of the changed behaviour?
- Would the tests actually fail if the logic regressed?
- Does every new public seam have a test in the convention its siblings use?
- Deterministic? No reliance on real network, wall-clock time, or ordering.`},
	L8: {L8, "Maintainability", `- Dead code; duplicated logic that should be unified.
- Magic numbers; names that contradict behaviour.
- Premature abstractions, or missing ones where complexity is genuinely high.`},
	L9: {L9, "Conventions & Project Rules", `- Read the rules files (CLAUDE.md, AGENTS.md, CONTRIBUTING*, .editorconfig, lint configs) at the repo root and in ancestor directories of changed files.
- Flag ONLY a clear violation you can pin to an exact changed line, quoting the exact rule and naming the rules file.
- No style preferences, no "spirit of the doc" inference.
- If no rules file governs the change, submit no findings.`},
}

// RulesFiles are the files whose presence at the repo root enables L9.
var RulesFiles = []string{"CLAUDE.md", "AGENTS.md", "CONTRIBUTING.md", ".editorconfig", "GEMINI.md"}
