# Subagent Evolution Roadmap

## Implementation review (2026-10-12)

- Phase 1: parallel tasks, chains, and batch waits are implemented, including
  directory results, timeout classification, and orchestration result rendering.
- Phase 2: Agent Hub, Alt+A, the active-job bar, terminal records, reliable token
  totals, and isolated child transcript viewing are implemented. Usage comes from
  Manager snapshots in memory and is persisted in metadata on completion. The Hub
  shows jobs started in this process for the current parent session.
  **Remaining: USD cost estimation.** Model configuration has no pricing source;
  unknown cost is not treated as free.
- Phase 3: `agent_wait.message`, an eight-entry FIFO, a limit of five injection
  batches, and actual injection counts are implemented. With a message and a zero
  or omitted `timeout_sec`, the tool returns `queued`; a positive timeout sends
  the message and then waits. Calls without a message retain their previous wait
  behavior. Injection counts are retained within the current process.

The original design follows. The current code is authoritative for file layout
and implementation details.

## Original baseline

Phi's subagent system has three built-in roles (explore/review/worker), runs in
process using goroutines, and is manually orchestrated through agent_spawn,
agent_wait, and agent_cancel. Child agents have no interactive input, and the TUI
only tracks progress.

Add no tools: extend the schemas of the existing three tools. The three phases
are ordered by priority, and each can be merged independently.

---

## Phase 1: Orchestration modes

**Goal:** Extend agent_spawn and agent_wait to support chains and parallel batches.

### 1.1 Extend agent_spawn with parallel tasks

Add a `tasks` field to `spawnInput`:

```go
type spawnInput struct {
    // Keep existing fields unchanged.
    Prompt      string `json:"prompt"`
    Description string `json:"description"`
    Role        string `json:"role"`
    WorkDir     string `json:"workdir"`
    TimeoutSec  int    `json:"timeout_sec"`

    // Parallel batch.
    Tasks []spawnTask `json:"tasks,omitempty"`
}

type spawnTask struct {
    Role        string `json:"role"`
    Task        string `json:"task"`
    Description string `json:"description,omitempty"`
}
```

Handler behavior:

- When `tasks` is nonempty, ignore the top-level `prompt/role/description` and
  spawn the batch.
- Create an independent job for each task and return
  `{ jobs: [{job_id, role, status, dir}] }`.
- Reuse `Manager.MaxConcurrent()` as the concurrency limit. On partial success,
  return the jobs that were started.
- When `tasks` is empty, retain the existing single-spawn behavior for full
  backward compatibility.

### 1.2 Extend agent_spawn with chain mode

Add a `chain` field to `spawnInput`:

```go
type spawnInput struct {
    // ... all fields above ...

    // Sequential chain.
    Chain []chainStep `json:"chain,omitempty"`
}

type chainStep struct {
    Role        string `json:"role"`
    Task        string `json:"task"`
    Description string `json:"description,omitempty"`
}
```

Handler behavior:

- When `chain` is nonempty, execute sequentially: spawn the first job, wait, read
  its summary, replace `{previous}` in the next task, then spawn the next job.
- Stop immediately if any step fails and return
  `{ chain: [{job_id, role, status, summary}], failed_at: int }`.
- Chains are synchronous: the handler blocks until completion or failure.
- Return the last step's summary as the main output.

Validation: `tasks` and `chain` cannot both be nonempty. `prompt` may be omitted
in tasks or chain mode.

### 1.3 Extend agent_wait with batch waiting

Add a `job_ids` field to `jobIDInput`:

```go
type jobIDInput struct {
    JobID      string   `json:"job_id,omitempty"`  // Existing single wait.
    JobIDs     []string `json:"job_ids,omitempty"` // New batch wait.
    TimeoutSec int      `json:"timeout_sec,omitempty"`
}
```

Handler behavior:

- When `job_ids` is nonempty, wait for all jobs to reach a terminal state.
- Return `{ results: [{job_id, role, status, summary, error}] }`.
- A timeout does not cancel jobs. Return terminal results and a
  `pending: [job_id, ...]` list.
- When `job_ids` is empty and `job_id` is nonempty, retain the existing single-wait
  behavior for full backward compatibility.

### Planned files

| File | Change |
|------|--------|
| `internal/tools/agenttool/agent.go` | Modify inputs and handler branches for tasks, chains, and batch waits |
| `internal/tools/agenttool/agent_s4_test.go` | Adapt tests for new fields |
| `internal/tools/agenttool/chain_test.go` | Add chain execution tests |
| `internal/tools/agenttool/parallel_test.go` | Add parallel task tests |
| `internal/tools/agenttool/waitall_test.go` | Add batch wait tests |

---

## Phase 2: Observability — Agent Hub

**Goal:** View and monitor all child agents' status and usage in the TUI in real time.

### 2.1 Usage tracking

Extend `internal/job/progress.go`:

```go
type Progress struct {
    // ... existing fields ...
    Usage *UsageDelta // nil = no usage info in this event
}

type UsageDelta struct {
    InputTokens  int
    OutputTokens int
    CacheRead    int
    CacheWrite   int
}
```

Modify `internal/agent/engine_runner.go`:

- Extract `e.Message.Usage` in the `session.AssistantMessageUpdate` branch and emit
  a `UsageDelta`.

Modify `internal/job/manager.go`:

- Add an accumulated `usage UsageDelta` field to `liveJob`.
- Accumulate usage in the `OnProgress` callback.
- Add a `Manager.Usage(jobID) UsageDelta` query method.

### 2.2 AgentHub component

Create `internal/components/agenthub/`.

**model.go** — data structures:

```go
type AgentRow struct {
    JobID     string
    Role      job.Role
    Status    job.Status
    Detail    string // Current tool or most recent event.
    Elapsed   time.Duration
    Usage     UsageSummary
    ParentID  string
}
```

Aggregate data from `SubagentStore` and `job.Manager`.

**view.go** — rendering:

- Each row: status icon, role, detail, elapsed time, and usage (tokens/cost).
- Status icons: running=spinner, completed=check, failed=cross, cancelled=slash.
- Footer: N running, M completed, total tokens.

### 2.3 TUI integration

- Toggle the Agent Hub overlay with `Alt+A`.
- Reuse the existing overlay pattern in `internal/tui/overlays/`.
- Show a pinned subagent bar above the editor while child agents are active,
  with a compact role/status line.
- Press Enter in the Hub to view the selected child transcript, reading JSONL
  files under `<job.Dir>/session/`.

### Planned files

| File | Change |
|------|--------|
| `internal/job/progress.go` | Add UsageDelta |
| `internal/job/manager.go` | Accumulate usage and add the Usage method |
| `internal/agent/engine_runner.go` | Emit usage |
| `internal/components/agenthub/model.go` | Create |
| `internal/components/agenthub/view.go` | Create |
| `internal/tui/overlays/agenthub.go` | Create |
| `internal/tui/controller/` | Add Alt+A binding |
| `internal/tui/transcript/subagent_store.go` | Aggregate usage |

---

## Phase 3: Interaction — Steering

**Goal:** Let the parent append instructions to a running child agent and redirect
its work. Use the message field on agent_wait without adding a tool.

### 3.1 Extend agent_wait with a steering message

Add a `message` field to `jobIDInput`:

```go
type jobIDInput struct {
    JobID      string   `json:"job_id,omitempty"`
    JobIDs     []string `json:"job_ids,omitempty"`
    TimeoutSec int      `json:"timeout_sec,omitempty"`
    Message    string   `json:"message,omitempty"` // Message to inject into the child.
}
```

Handler behavior:

- When `message` is nonempty and `job_id` identifies a running job, inject the
  message into the child agent.
- Continue waiting after injection, or return "message delivered" immediately,
  depending on `timeout_sec`.
- Return an error if the child agent is not running.

### 3.2 Engine loop changes

Modify `internal/job/manager.go`:

- Add `mailbox chan string` to `liveJob`, buffered with capacity 8.
- Add `Manager.Send(jobID, msg) error` to write to the mailbox and return an error
  if the job is not running.

Modify `internal/agent/engine_runner.go`:

- After each assistant message completes, check the mailbox with a nonblocking
  select in the run loop.
- When a message is available, inject it through `session.AddUser(msg)` and
  continue the loop.
- Limit injection rounds to five by default to prevent an infinite loop.

Modify `internal/agent/engine.go`:

- Add an `OnRoundEnd func() []string` callback to `LoopOpts`.
- Have EngineRunner implement the callback by draining messages from the mailbox.
- Call it at the end of each round; inject a nonempty result as new user turns.

### 3.3 Enhanced agent_wait results

Add the following field to the wait response JSON:

- `injected_messages: int` — the total number of steering messages injected into
  the job.

### Planned files

| File | Change |
|------|--------|
| `internal/job/manager.go` | Add mailbox and Send method |
| `internal/agent/engine.go` | Add OnRoundEnd callback |
| `internal/agent/engine_runner.go` | Consume mailbox messages |
| `internal/tools/agenttool/agent.go` | Handle wait messages and return injected_messages |
| `internal/tools/agenttool/steer_test.go` | Add steering tests |

---

## Dependencies

```text
Phase 1 (orchestration) — Independent; can be implemented first.
Phase 2 (Agent Hub)     — Independent; can run in parallel with Phase 1.
Phase 3 (steering)      — Prefer after Phase 2, with the Hub as a future steering UI entry point.
```

## Out of scope

- New tools: extend agent_spawn, agent_wait, and agent_cancel instead.
- Custom agent definition files: keep the three built-in roles.
- Process-level isolation.
- Direct communication between child agents (peer messaging).
- Advanced model-switching strategies such as prewalk or advisor.
