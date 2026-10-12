package transcript

import (
	"slices"
	"sync"

	"github.com/pulseaiclub/phi/internal/components/block"
	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/session"
	"github.com/pulseaiclub/phi/internal/tools"
)

// SubagentStore holds UI-only nested tool trees for agent_* rows.
// It is not part of the session / LLM transcript.
type SubagentStore struct {
	mu       sync.Mutex
	byParent map[string][]*subagentView
	byJob    map[string]*subagentView
}

type subagentView struct {
	JobID           string
	ParentToolUseID string
	Children        []block.ChildTool
	childIdx        map[string]int
	Summary         string
}

// NewSubagentStore creates an empty store.
func NewSubagentStore() *SubagentStore {
	return &SubagentStore{
		byParent: make(map[string][]*subagentView),
		byJob:    make(map[string]*subagentView),
	}
}

// Bind links a job id to the parent agent tool_use id.
func (s *SubagentStore) Bind(jobID, parentToolUseID string) {
	if s == nil || jobID == "" || parentToolUseID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.viewLocked(jobID, parentToolUseID)
	v.JobID = jobID
	v.ParentToolUseID = parentToolUseID
}

func (s *SubagentStore) viewLocked(jobID, parentToolUseID string) *subagentView {
	v := s.byJob[jobID]
	if jobID == "" && len(s.byParent[parentToolUseID]) == 1 {
		v = s.byParent[parentToolUseID][0]
	}
	if v == nil && parentToolUseID != "" {
		// Progress without a job ID can arrive before the result binds it.
		for _, candidate := range s.byParent[parentToolUseID] {
			if candidate.JobID == "" {
				v = candidate
				break
			}
		}
	}
	if v == nil {
		v = &subagentView{childIdx: make(map[string]int)}
	}
	if jobID != "" {
		v.JobID = jobID
		s.byJob[jobID] = v
	}
	if parentToolUseID != "" {
		v.ParentToolUseID = parentToolUseID
		// Keep spawn and wait aliases, with each job included once in binding order.
		if slices.Contains(s.byParent[parentToolUseID], v) {
			return v
		}
		s.byParent[parentToolUseID] = append(s.byParent[parentToolUseID], v)
	}
	return v
}

// ApplyProgress upserts a child tool row under the parent agent tool.
// Returns true when the nested tree view actually changed.
func (s *SubagentStore) ApplyProgress(p job.Progress) bool {
	if s == nil || (p.JobID == "" && p.ParentToolUseID == "") || p.Name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.viewLocked(p.JobID, p.ParentToolUseID)
	key := p.ToolUseID
	if key == "" {
		key = p.Name + "\x00" + p.Detail
	}
	child := block.ChildTool{
		Name:   p.Name,
		Detail: p.Detail,
		Status: uiToolStatus(session.ParseToolStatus(p.Status)),
	}
	if i, ok := v.childIdx[key]; ok {
		if v.Children[i] == child {
			return false
		}
		v.Children[i] = child
		return true
	}
	v.childIdx[key] = len(v.Children)
	v.Children = append(v.Children, child)
	return true
}

// ApplyResult records terminal summary / binding from agent tool JSON output.
func (s *SubagentStore) ApplyResult(parentToolUseID string, r tools.AgentResult) {
	if s == nil || !r.OK {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.viewLocked(r.JobID, parentToolUseID)
	if sum := r.RenderableSummary(); sum != "" {
		v.Summary = sum
	}
}

// Children returns a copy of nested tools for a parent tool_use id.
func (s *SubagentStore) Children(parentToolUseID string) []block.ChildTool {
	if s == nil || parentToolUseID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var children []block.ChildTool
	for _, v := range s.byParent[parentToolUseID] {
		children = append(children, v.Children...)
	}
	return children
}

// ChildrenByJob returns a copy of nested tools for a job id.
func (s *SubagentStore) ChildrenByJob(jobID string) []block.ChildTool {
	if s == nil || jobID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.byJob[jobID]
	if v == nil {
		return nil
	}
	return append([]block.ChildTool(nil), v.Children...)
}
