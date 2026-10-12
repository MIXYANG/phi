package overlays

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pulseaiclub/xui"

	"github.com/pulseaiclub/phi/internal/components"
	"github.com/pulseaiclub/phi/internal/components/agenthub"
	"github.com/pulseaiclub/phi/internal/components/status"
	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/session"
	"github.com/pulseaiclub/phi/internal/tui/transcript"
)

type jobSnapshots interface{ Snapshot(string) []job.Info }

// AgentHub owns UI-only selection and child replay, never the parent session.
type AgentHub struct {
	source   jobSnapshots
	parentID string
	rows     []agenthub.AgentRow
	infos    map[string]job.Info
	details  map[string]string
	active   bool
	selected int
	scroll   int
	height   int
	child    *transcript.TranscriptPane
	childID  string
	loadErr  string
	file     string
	modTime  time.Time
	fileSize int64
	lastRead time.Time
}

func NewAgentHub(source jobSnapshots) *AgentHub {
	return &AgentHub{source: source, details: make(map[string]string)}
}
func (h *AgentHub) Active() bool { return h != nil && h.active }
func (h *AgentHub) Close()       { h.active = false; h.child = nil; h.childID = "" }
func (h *AgentHub) Toggle() {
	if h.active {
		h.Close()
	} else {
		h.active = true
	}
}

func (h *AgentHub) ApplyProgress(p job.Progress) {
	if p.Name != "" {
		h.details[p.JobID] = p.Name + " " + p.Detail
	}
}

// Refresh uses retained manager usage: progress delivery may coalesce or drop.
func (h *AgentHub) Refresh(parentID string) {
	if h.parentID != parentID {
		h.Close()
		h.selected = 0
		h.scroll = 0
		h.details = make(map[string]string)
		h.parentID = parentID
	}
	var selectedID string
	if h.selected < len(h.rows) {
		selectedID = h.rows[h.selected].JobID
	}
	h.rows = nil
	h.infos = make(map[string]job.Info)
	if h.source == nil || parentID == "" {
		return
	}
	infos := h.source.Snapshot(parentID)
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].CreatedAt.Equal(infos[j].CreatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].CreatedAt.Before(infos[j].CreatedAt)
	})
	for _, info := range infos {
		start := info.StartedAt
		if start.IsZero() {
			start = info.CreatedAt
		}
		end := info.FinishedAt
		if end.IsZero() {
			end = time.Now()
		}
		detail := h.details[info.ID]
		if detail == "" {
			detail = info.Description
		}
		if info.Error != "" {
			detail = info.Error
		}
		u := info.Usage
		h.rows = append(
			h.rows,
			agenthub.AgentRow{
				JobID:   info.ID,
				Role:    info.Role,
				Status:  info.Status,
				Detail:  detail,
				Elapsed: end.Sub(start),
				Usage: agenthub.UsageSummary{
					InputTokens:  u.InputTokens,
					OutputTokens: u.OutputTokens,
					CacheRead:    u.CacheRead,
					CacheWrite:   u.CacheWrite,
					CostUSD:      u.CostUSD,
				},
			},
		)
		h.infos[info.ID] = info
		if info.ID == selectedID {
			h.selected = len(h.rows) - 1
		}
	}
	h.selected = min(h.selected, max(0, len(h.rows)-1))
}
func (h *AgentHub) PinnedBar(glyph string) string { return agenthub.PinnedBar(h.rows, glyph) }
func (h *AgentHub) LiveCount() int {
	n := 0
	for _, r := range h.rows {
		if !r.Status.Terminal() {
			n++
		}
	}
	return n
}

func (h *AgentHub) Handle(ctx *components.EventContext, ev xui.Event, theme components.Theme, spin *status.Spinner) {
	ctx.ConsumeAndRedraw()
	ke, ok := ev.(xui.KeyEvent)
	if !ok || !ke.Press {
		return
	}
	if ke.Code == xui.KeyEscape {
		if h.childID != "" {
			h.child = nil
			h.childID = ""
			h.loadErr = ""
		} else {
			h.Close()
		}
		return
	}
	if h.childID != "" {
		if ke.Rune == 'r' {
			h.lastRead = time.Time{}
			h.file = ""
		}
		if h.child != nil {
			h.child.HandlePageKey(ctx, ke)
		}
		return
	}
	switch ke.Code {
	case xui.KeyUp:
		h.selected--
	case xui.KeyDown:
		h.selected++
	case xui.KeyPageUp:
		h.selected -= max(1, h.height-3)
	case xui.KeyPageDown:
		h.selected += max(1, h.height-3)
	case xui.KeyHome:
		h.selected = 0
	case xui.KeyEnd:
		h.selected = len(h.rows) - 1
	case xui.KeyEnter:
		if len(h.rows) > 0 {
			h.childID = h.rows[h.selected].JobID
			h.child = transcript.NewTranscriptPane(theme, spin, "")
			h.file = ""
			h.lastRead = time.Time{}
			h.loadErr = ""
			h.refreshChild()
		}
	default:
		if ke.Rune == 'j' {
			h.selected++
		}
		if ke.Rune == 'k' {
			h.selected--
		}
	}
	h.selected = max(0, min(h.selected, len(h.rows)-1))
}

// Poll only the isolated child file and replay when its stat changes. A writer
// may be between records; failed reads keep the last complete view available.
func (h *AgentHub) refreshChild() {
	if h.childID == "" || time.Since(h.lastRead) < time.Second {
		return
	}
	h.lastRead = time.Now()
	info, ok := h.infos[h.childID]
	if !ok {
		h.loadErr = "Job is no longer tracked."
		return
	}
	files, err := filepath.Glob(filepath.Join(info.Dir, "session", "*.jsonl"))
	if err != nil || len(files) == 0 {
		h.loadErr = "Transcript not written yet. Retrying…"
		return
	}
	path := files[len(files)-1]
	stat, err := os.Stat(path)
	if err != nil {
		h.loadErr = fmt.Sprintf("Read transcript: %v", err)
		return
	}
	if path == h.file && stat.ModTime().Equal(h.modTime) && stat.Size() == h.fileSize {
		return
	}
	sm, err := session.OpenSession(path)
	if err != nil {
		h.loadErr = fmt.Sprintf("Read transcript: %v (r to retry)", err)
		return
	}
	bottom := h.child.AtBottom()
	h.child.LoadReplay(session.ReplaySnapshot(sm.BuildContext(), nil))
	h.child.Sync()
	if bottom {
		h.child.StickToBottom()
	}
	h.file = path
	h.modTime = stat.ModTime()
	h.fileSize = stat.Size()
	h.loadErr = ""
}

func (h *AgentHub) Draw(ctx components.DrawContext, theme components.Theme, glyph string) components.Surface {
	w, ht := max(0, ctx.Max.Width), max(0, ctx.Max.Height)
	h.height = ht
	surf := components.NewSurface(w, ht, nil)
	title := "Agent Hub · ↑↓/j/k move · Enter transcript · Alt+A/Esc close"
	if h.childID != "" {
		title = "Agent transcript · " + h.childID + " · PgUp/PgDn scroll · Esc back · r refresh"
	}
	surf.Print(0, 0, title, theme.ToolName, ctx.Method)
	if h.childID != "" {
		h.refreshChild()
		h.child.SetTheme(theme)
		if ht > 2 && !h.child.IsEmpty() {
			surf.Children = append(
				surf.Children,
				components.SubSurface{Origin: components.Point{Y: 1}, Surface: h.child.Draw(ctx, w, ht-2)},
			)
		}
		if h.loadErr != "" {
			surf.Print(0, max(0, ht-1), h.loadErr, theme.Muted, ctx.Method)
		} else if h.child.IsEmpty() {
			surf.Print(0, 1, "No transcript messages yet.", theme.Muted, ctx.Method)
		}
		return surf
	}
	if len(h.rows) == 0 {
		surf.Print(0, 1, "No sub-agents for this session.", theme.Muted, ctx.Method)
		return surf
	}
	visible := max(1, ht-2)
	if h.selected < h.scroll {
		h.scroll = h.selected
	}
	if h.selected >= h.scroll+visible {
		h.scroll = h.selected - visible + 1
	}
	for i := h.scroll; i < len(h.rows) && i < h.scroll+visible; i++ {
		prefix := "  "
		style := theme.Foreground
		if i == h.selected {
			prefix = "› "
			style = theme.ToolName
		}
		surf.Print(0, i-h.scroll+1, prefix+agenthub.RowLine(h.rows[i], glyph), style, ctx.Method)
	}
	surf.Print(0, max(0, ht-1), agenthub.Summary(h.rows), theme.Muted, ctx.Method)
	return surf
}
