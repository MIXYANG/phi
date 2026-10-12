package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pulseaiclub/phi/internal/tools/tooldef"

	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/llm"
)

const agentSummaryLimit = 12000 // bytes, keep parent context small

const agentLaunchGuidance = `Launch specialized sub-agents. Pick a role:

- explore (default): no write/edit; full bash except hard denies. Multi-hop recon / unknown location — keep tool noise out of this context.
- review: same tools as explore; diffs, checks, quality/security report — do not edit.
- worker: read/write/edit + bash; implement a scoped, self-contained change and verify. Not for open-ended exploration.

When NOT to use any sub-agent:
- Exact file path already known — use read yourself
- Exact symbol like "class Foo" — use grep yourself
- Tiny local edit — edit/write yourself

Modes (provide exactly one):
1. Single: {prompt, role} — async, returns job_id. Use agent_wait for the summary.
2. Parallel: {tasks: [{role, task}]} — spawns multiple jobs at once, returns all job_ids.
3. Chain: {chain: [{role, task}]} — sequential; each step's summary replaces {previous} in the next task. Blocks until done.

General rules:
- Stateless: put a highly detailed, self-contained prompt and say what the final summary must include.
- You only receive the final summary. Summarize for the user if needed.
- Sub-agents cannot spawn further agents. Do not put secrets in the prompt.
- Verify before relying on a worker's edits in follow-up work.`

// AgentDeps wires sub-agent tools to a process-level [job.Manager].
// ParentID/WorkDir are read at call time (session may change via /resume).
type AgentDeps struct {
	Manager  *job.Manager
	ParentID func() string
	WorkDir  func() string
}

// AgentTools returns agent_spawn / list / wait / cancel.
// Depth is forced to 0; ParentID comes from ParentID(), not model args.
func AgentTools(deps AgentDeps) []tooldef.Tool {
	if deps.Manager == nil {
		return nil
	}
	if deps.ParentID == nil {
		deps.ParentID = func() string { return "" }
	}
	if deps.WorkDir == nil {
		deps.WorkDir = func() string { return "" }
	}
	return []tooldef.Tool{
		agentSpawnTool(deps),
		agentWaitTool(deps),
		agentCancelTool(deps),
	}
}

func agentSpawnTool(deps AgentDeps) tooldef.Tool {
	return tooldef.NewTool(
		tooldef.WithDefinition(llm.ToolDefinition{
			Name: "agent_spawn",
			Description: fmt.Sprintf(agentLaunchGuidance+`

Single mode starts asynchronously and returns job_id immediately. Use agent_wait for the summary.
Chain mode blocks until all steps complete.

Concurrency cap: at most %d sub-agents run concurrently; spawning more fails (jobs are not queued).`, deps.Manager.MaxConcurrent()),
			Params: &llm.FunctionParameters{
				Type: "object",
				Properties: llm.Object{
					"prompt": llm.Object{
						"type":        "string",
						"description": "Self-contained task (single mode). Include context, scope, and exactly what the final summary must return.",
					},
					"description": llm.Object{
						"type":        "string",
						"description": "Very short label for the UI / job list (e.g. \"find auth config\").",
					},
					"role": llm.Object{
						"type":        "string",
						"description": "explore (default) | review | worker (single mode).",
						"enum":        []string{"explore", "review", "worker"},
					},
					"workdir": llm.Object{
						"type":        "string",
						"description": "Working directory for the sub-agent (default: parent session cwd).",
					},
					"timeout_sec": llm.Object{
						"type":        "integer",
						"description": "Optional run timeout in seconds for each job (not wait).",
					},
					"tasks": llm.Object{
						"type":        "array",
						"description": "Parallel mode: array of {role, task, description}. Spawns all at once, returns all job_ids.",
						"items": llm.Object{
							"type": "object",
							"properties": llm.Object{
								"role": llm.Object{
									"type": "string",
									"enum": []string{"explore", "review", "worker"},
								},
								"task":        llm.Object{"type": "string"},
								"description": llm.Object{"type": "string"},
							},
							"required": []string{"task"},
						},
					},
					"chain": llm.Object{
						"type":        "array",
						"description": "Chain mode: array of {role, task}. Sequential; {previous} in each task is replaced by the prior step's summary. Blocks until done.",
						"items": llm.Object{
							"type": "object",
							"properties": llm.Object{
								"role": llm.Object{
									"type": "string",
									"enum": []string{"explore", "review", "worker"},
								},
								"task":        llm.Object{"type": "string"},
								"description": llm.Object{"type": "string"},
							},
							"required": []string{"task"},
						},
					},
				},
			},
		}),
		tooldef.WithDetail(spawnDetailRaw),
		tooldef.WithRun(func(ctx context.Context, raw json.RawMessage) (tooldef.Result, error) {
			var in spawnInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return tooldef.Result{}, err
			}

			hasTasks := len(in.Tasks) > 0
			hasChain := len(in.Chain) > 0
			if hasTasks && hasChain {
				return tooldef.Result{}, errors.New("agent_spawn: tasks and chain are mutually exclusive")
			}

			switch {
			case hasTasks:
				return handleSpawnTasks(ctx, deps, in)
			case hasChain:
				return handleSpawnChain(ctx, deps, in)
			default:
				return handleSpawnSingle(ctx, deps, in)
			}
		}),
	)
}

type spawnInput struct {
	Prompt      string      `json:"prompt"`
	Description string      `json:"description"`
	Role        string      `json:"role"`
	WorkDir     string      `json:"workdir"`
	TimeoutSec  int         `json:"timeout_sec"`
	Tasks       []spawnTask `json:"tasks"`
	Chain       []chainStep `json:"chain"`
}

type spawnTask struct {
	Role        string `json:"role"`
	Task        string `json:"task"`
	Description string `json:"description"`
}

type chainStep struct {
	Role        string `json:"role"`
	Task        string `json:"task"`
	Description string `json:"description"`
}

func spawnDetailRaw(raw json.RawMessage) string {
	var in spawnInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "spawn"
	}
	switch {
	case len(in.Tasks) > 0:
		return fmt.Sprintf("parallel · %d tasks", len(in.Tasks))
	case len(in.Chain) > 0:
		return fmt.Sprintf("chain · %d steps", len(in.Chain))
	default:
		label := strings.TrimSpace(in.Description)
		if label == "" {
			label = truncateRunes(in.Prompt, 80)
		}
		return roleDetail(in.Role, label)
	}
}

func handleSpawnSingle(ctx context.Context, deps AgentDeps, in spawnInput) (tooldef.Result, error) {
	if in.Prompt == "" {
		return tooldef.Result{}, errors.New("agent_spawn: prompt is required for single mode")
	}
	role, err := job.ParseRole(in.Role)
	if err != nil {
		return tooldef.Result{}, err
	}
	wd := strings.TrimSpace(in.WorkDir)
	if wd == "" {
		wd = deps.WorkDir()
	}
	req := job.SpawnRequest{
		Prompt:          in.Prompt,
		Description:     in.Description,
		ParentID:        deps.ParentID(),
		ParentToolUseID: tooldef.ToolCallID(ctx),
		Depth:           0,
		Role:            role,
		WorkDir:         wd,
	}
	if in.TimeoutSec > 0 {
		req.Timeout = time.Duration(in.TimeoutSec) * time.Second
	}
	info, err := deps.Manager.Spawn(ctx, req)
	if err != nil {
		return tooldef.Result{}, err
	}
	body := mustJSON(map[string]any{
		"job_id":      info.ID,
		"status":      info.Status,
		"role":        info.Role,
		"dir":         info.Dir,
		"result_path": info.ResultPath,
	})
	return tooldef.Result{Content: body, Detail: roleDetail(string(info.Role), info.ID), Output: body}, nil
}

func handleSpawnTasks(ctx context.Context, deps AgentDeps, in spawnInput) (tooldef.Result, error) {
	wd := strings.TrimSpace(in.WorkDir)
	if wd == "" {
		wd = deps.WorkDir()
	}

	type jobEntry struct {
		JobID  string     `json:"job_id"`
		Role   job.Role   `json:"role"`
		Status job.Status `json:"status"`
		Dir    string     `json:"dir,omitempty"`
		Error  string     `json:"error,omitempty"`
	}

	jobs := make([]jobEntry, 0, len(in.Tasks))
	for _, t := range in.Tasks {
		role, err := job.ParseRole(t.Role)
		if err != nil {
			jobs = append(jobs, jobEntry{Role: job.NormalizeRole(t.Role), Error: err.Error()})
			continue
		}
		req := job.SpawnRequest{
			Prompt:          t.Task,
			Description:     t.Description,
			ParentID:        deps.ParentID(),
			ParentToolUseID: tooldef.ToolCallID(ctx),
			Depth:           0,
			Role:            role,
			WorkDir:         wd,
		}
		if in.TimeoutSec > 0 {
			req.Timeout = time.Duration(in.TimeoutSec) * time.Second
		}
		info, err := deps.Manager.Spawn(ctx, req)
		if err != nil {
			jobs = append(jobs, jobEntry{Role: role, Error: err.Error()})
			continue
		}
		jobs = append(jobs, jobEntry{JobID: info.ID, Role: info.Role, Status: info.Status, Dir: info.Dir})
	}

	body := mustJSON(map[string]any{"jobs": jobs})
	return tooldef.Result{Content: body, Detail: fmt.Sprintf("parallel · %d tasks", len(in.Tasks)), Output: body}, nil
}

func handleSpawnChain(ctx context.Context, deps AgentDeps, in spawnInput) (tooldef.Result, error) {
	wd := strings.TrimSpace(in.WorkDir)
	if wd == "" {
		wd = deps.WorkDir()
	}

	type chainResult struct {
		JobID   string   `json:"job_id"`
		Role    job.Role `json:"role"`
		Status  string   `json:"status"`
		Summary string   `json:"summary,omitempty"`
		Error   string   `json:"error,omitempty"`
	}

	results := make([]chainResult, 0, len(in.Chain))
	previous := ""
	failedAt := -1

	for i, step := range in.Chain {
		role, err := job.ParseRole(step.Role)
		if err != nil {
			results = append(
				results,
				chainResult{Role: job.NormalizeRole(step.Role), Status: string(job.StatusFailed), Error: err.Error()},
			)
			failedAt = i
			break
		}

		taskPrompt := strings.ReplaceAll(step.Task, "{previous}", previous)
		req := job.SpawnRequest{
			Prompt:          taskPrompt,
			Description:     step.Description,
			ParentID:        deps.ParentID(),
			ParentToolUseID: tooldef.ToolCallID(ctx),
			Depth:           0,
			Role:            role,
			WorkDir:         wd,
		}
		if in.TimeoutSec > 0 {
			req.Timeout = time.Duration(in.TimeoutSec) * time.Second
		}

		info, err := deps.Manager.Spawn(ctx, req)
		if err != nil {
			results = append(results, chainResult{Role: role, Status: string(job.StatusFailed), Error: err.Error()})
			failedAt = i
			break
		}

		// Wait for this step to complete before proceeding.
		waitRes, err := deps.Manager.Wait(ctx, info.ID)
		if err != nil {
			// Jobs outlive tool calls; an interrupted wait is not a failed job.
			if current, getErr := deps.Manager.Get(context.WithoutCancel(ctx), info.ID); getErr == nil {
				info = current
			}
			results = append(
				results,
				chainResult{JobID: info.ID, Role: info.Role, Status: string(info.Status), Error: err.Error()},
			)
			failedAt = i
			break
		}

		cr := chainResult{
			JobID:   waitRes.Info.ID,
			Role:    waitRes.Info.Role,
			Status:  string(waitRes.Info.Status),
			Summary: truncateBytes(waitRes.Summary, agentSummaryLimit),
		}
		if waitRes.Info.Error != "" {
			cr.Error = waitRes.Info.Error
		}
		results = append(results, cr)

		if !waitRes.Info.Status.Terminal() || waitRes.Info.Status == job.StatusFailed ||
			waitRes.Info.Status == job.StatusCancelled ||
			waitRes.Info.Status == job.StatusTimedOut {
			failedAt = i
			break
		}

		previous = waitRes.Summary
	}

	lastSummary := ""
	if len(results) > 0 {
		lastSummary = results[len(results)-1].Summary
	}

	body := mustJSON(map[string]any{
		"chain":     results,
		"failed_at": failedAt,
		"summary":   lastSummary,
	})
	return tooldef.Result{Content: body, Detail: fmt.Sprintf("chain · %d steps", len(in.Chain)), Output: body}, nil
}

// jobIDInput is the shared payload of agent_wait and agent_cancel.
// agent_cancel uses only job_id; agent_wait also supports job_ids for batch.
type jobIDInput struct {
	JobID      string   `json:"job_id"`
	JobIDs     []string `json:"job_ids"`
	TimeoutSec int      `json:"timeout_sec"`
	Message    *string  `json:"message"`
}

func jobIDDetail(in jobIDInput) string {
	if len(in.JobIDs) > 0 {
		return fmt.Sprintf("%d jobs", len(in.JobIDs))
	}
	return in.JobID
}

// roleDetail is the one-line TUI suffix: "explore · find auth".
func roleDetail(role, rest string) string {
	r := string(job.NormalizeRole(role))
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return r
	}
	return r + " · " + rest
}

func agentWaitTool(deps AgentDeps) tooldef.Tool {
	return tooldef.NewTool(
		tooldef.WithDefinition(llm.ToolDefinition{
			Name: "agent_wait",
			Description: `Block until sub-agent job(s) reach terminal status and return their summaries.

Single mode: {job_id} — wait for one job.
Batch mode: {job_ids: [...]} — wait for multiple jobs within one timeout budget.
Steering: {job_id, message, timeout_sec: 0} queues a message and returns immediately.
With a positive timeout, queue the message then wait. Steering requires a running job.
Queued messages are not necessarily injected; injected_messages counts actual delivery.

timeout_sec only limits how long this wait blocks — it does NOT cancel jobs.
Without a message, zero or omitted timeout waits until completion.
Use agent_cancel to stop a running job.`,
			Params: &llm.FunctionParameters{
				Type: "object",
				Properties: llm.Object{
					"job_id": llm.Object{
						"type":        "string",
						"description": "Job id from agent_spawn (single mode).",
					},
					"message": llm.Object{
						"type":        "string",
						"description": "Steering message to queue for a running job. Requires job_id; cannot be combined with job_ids.",
					},
					"job_ids": llm.Object{
						"type":        "array",
						"description": "Job ids to wait for (batch mode), sharing one timeout budget.",
						"items":       llm.Object{"type": "string"},
					},
					"timeout_sec": llm.Object{
						"type":        "integer",
						"description": "Max seconds to wait, nonnegative (does not cancel jobs). With message, zero or omitted returns a queued acknowledgment; without message, waits until completion.",
						"minimum":     0,
					},
				},
			},
		}),
		tooldef.WithDetail(jobIDDetail),
		tooldef.WithRun(func(ctx context.Context, raw json.RawMessage) (tooldef.Result, error) {
			var in jobIDInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return tooldef.Result{}, err
			}
			if in.TimeoutSec < 0 {
				return tooldef.Result{}, errors.New("agent_wait: timeout_sec must be nonnegative")
			}
			if in.Message != nil {
				if in.JobIDs != nil || strings.TrimSpace(in.JobID) == "" {
					return tooldef.Result{}, errors.New(
						"agent_wait: message requires job_id and cannot be combined with job_ids",
					)
				}
				if err := deps.Manager.Send(in.JobID, *in.Message); err != nil {
					return tooldef.Result{}, err
				}
				if in.TimeoutSec == 0 {
					body := mustJSON(map[string]any{
						"job_id": in.JobID, "queued": true,
						"injected_messages": deps.Manager.InjectedMessages(in.JobID),
					})
					return tooldef.Result{Content: body, Detail: "message queued", Output: body}, nil
				}
			}
			if len(in.JobIDs) > 0 {
				return handleWaitBatch(ctx, deps, in)
			}

			// Single mode.
			if in.JobID == "" {
				return tooldef.Result{}, errors.New("agent_wait: job_id or job_ids is required")
			}

			waitCtx := ctx
			var cancel context.CancelFunc
			if in.TimeoutSec > 0 {
				waitCtx, cancel = context.WithTimeout(ctx, time.Duration(in.TimeoutSec)*time.Second)
				defer cancel()
			}

			res, err := deps.Manager.Wait(waitCtx, in.JobID)
			if err != nil && in.TimeoutSec > 0 && errors.Is(err, job.ErrWaitTimeout) {
				info, getErr := deps.Manager.Get(ctx, in.JobID)
				if getErr != nil {
					return tooldef.Result{}, getErr
				}
				if !info.Status.Terminal() {
					body := mustJSON(map[string]any{
						"job_id": info.ID, "status": info.Status,
						"role": info.Role, "error": info.Error,
						"injected_messages": deps.Manager.InjectedMessages(info.ID),
					})
					return tooldef.Result{
						Content: body,
						Detail:  roleDetail(string(info.Role), string(info.Status)),
						Output:  body,
					}, nil
				}
				res, err = deps.Manager.Wait(ctx, in.JobID)
			}
			if err != nil {
				return tooldef.Result{}, err
			}

			summary := truncateBytes(res.Summary, agentSummaryLimit)
			body := mustJSON(map[string]any{
				"job_id":            res.Info.ID,
				"status":            res.Info.Status,
				"role":              res.Info.Role,
				"error":             res.Info.Error,
				"result_path":       res.Info.ResultPath,
				"summary":           summary,
				"injected_messages": deps.Manager.InjectedMessages(res.Info.ID),
			})
			return tooldef.Result{
				Content: body,
				Detail:  roleDetail(string(res.Info.Role), string(res.Info.Status)),
				Output:  body,
			}, nil
		}),
	)
}

func handleWaitBatch(ctx context.Context, deps AgentDeps, in jobIDInput) (tooldef.Result, error) {
	waitCtx := ctx
	var cancel context.CancelFunc
	if in.TimeoutSec > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(in.TimeoutSec)*time.Second)
		defer cancel()
	}

	type waitEntry struct {
		InjectedMessages int      `json:"injected_messages"`
		JobID            string   `json:"job_id"`
		Role             job.Role `json:"role"`
		Status           string   `json:"status"`
		Summary          string   `json:"summary,omitempty"`
		Error            string   `json:"error,omitempty"`
	}

	results := make([]waitEntry, 0, len(in.JobIDs))
	pending := make([]string, 0)

	for _, id := range in.JobIDs {
		res, err := deps.Manager.Wait(waitCtx, id)
		if err != nil && in.TimeoutSec > 0 && errors.Is(err, job.ErrWaitTimeout) {
			info, getErr := deps.Manager.Get(ctx, id)
			switch {
			case getErr != nil:
				err = getErr
			case info.Status.Terminal():
				// Completion may race the deadline; keep its summary and exclude it from pending.
				res, err = deps.Manager.Wait(ctx, id)
			default:
				pending = append(pending, id)
				continue
			}
		}
		if err != nil {
			results = append(results, waitEntry{
				JobID: id, Status: "error", Error: err.Error(),
				InjectedMessages: deps.Manager.InjectedMessages(id),
			})
			continue
		}
		results = append(results, waitEntry{
			JobID:            res.Info.ID,
			Role:             res.Info.Role,
			Status:           string(res.Info.Status),
			Summary:          truncateBytes(res.Summary, agentSummaryLimit),
			Error:            res.Info.Error,
			InjectedMessages: deps.Manager.InjectedMessages(id),
		})
	}

	body := mustJSON(map[string]any{
		"results": results,
		"pending": pending,
	})
	return tooldef.Result{
		Content: body,
		Detail:  fmt.Sprintf("batch · %d jobs", len(in.JobIDs)),
		Output:  body,
	}, nil
}

func agentCancelTool(deps AgentDeps) tooldef.Tool {
	return tooldef.NewTool(
		tooldef.WithDefinition(llm.ToolDefinition{
			Name:        "agent_cancel",
			Description: `Cancel a running or starting sub-agent job and wait until it stops.`,
			Params: &llm.FunctionParameters{
				Type: "object",
				Properties: llm.Object{
					"job_id": llm.Object{
						"type": "string",
					},
				},
				Required: []string{"job_id"},
			},
		}),
		tooldef.WithDetail(jobIDDetail),
		tooldef.WithRun(func(ctx context.Context, input json.RawMessage) (tooldef.Result, error) {
			if err := deps.Manager.HandleCancel(ctx, input); err != nil {
				return tooldef.Result{}, err
			}
			body := mustJSON(map[string]any{"ok": true})
			return tooldef.Result{Content: body, Detail: "cancelled", Output: body}, nil
		}),
	)
}

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func truncateBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "\n…(truncated)"
}

func truncateRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	var b strings.Builder
	i := 0
	for _, r := range s {
		if i >= n {
			break
		}
		b.WriteRune(r)
		i++
	}
	b.WriteString("…")
	return b.String()
}
