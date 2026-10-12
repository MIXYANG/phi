package job

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options configures a [Manager].
type Options struct {
	Root          string // required: jobs directory
	Runner        Runner // required
	MaxConcurrent int    // default 4; Spawn returns [ErrBusy] when full (no queue)
	MaxDepth      int    // default 1 (children cannot spawn further)
	Recovery      RecoveryMode
	// OnStoreError is called when a disk write fails after the job is live.
	// Create/Spawn still return the create error directly.
	OnStoreError func(op, jobID string, err error)
}

// Manager owns in-process job lifecycles and a disk store.
type Manager struct {
	store         *store
	runner        Runner
	maxConcurrent int
	maxDepth      int
	onStoreError  func(op, jobID string, err error)

	mu        sync.Mutex
	closed    bool
	slots     chan struct{}
	jobs      map[string]*liveJob
	snapshots map[string]Info // all jobs spawned in this process, including terminal jobs
	subs      []*subscriber
	injected  map[string]int // retained after live jobs are removed; process-local
}

type liveJob struct {
	meta            Meta
	parentToolUseID string
	cancel          context.CancelFunc
	done            chan struct{}
	mailbox         chan string
	steeringClosed  bool
	steeringRounds  int
	steeringPending int
	runCtx          context.Context
}

// subscriber is one progress channel. closed is guarded by m.mu: emitProgress
// sends while holding m.mu, and cancel/Close mark closed and close the channel
// under the same lock, so a send can never race a channel close.
type subscriber struct {
	ch     chan Progress
	closed bool
}

// New creates a Manager. Root and Runner are required.
//
// By default ([RecoverMarkFailed]), leftover starting/running jobs on disk are
// marked failed so a process restart does not leave Wait/Cancel zombies.
func New(opts Options) (*Manager, error) {
	if opts.Runner == nil {
		return nil, fmt.Errorf("%w: Runner is required", ErrInvalid)
	}
	st, err := newStore(opts.Root)
	if err != nil {
		return nil, err
	}
	maxC := opts.MaxConcurrent
	if maxC <= 0 {
		maxC = 4
	}
	maxD := opts.MaxDepth
	if maxD <= 0 {
		maxD = 1
	}
	m := &Manager{
		store:         st,
		runner:        opts.Runner,
		maxConcurrent: maxC,
		maxDepth:      maxD,
		onStoreError:  opts.OnStoreError,
		slots:         make(chan struct{}, maxC),
		jobs:          make(map[string]*liveJob),
		snapshots:     make(map[string]Info),
		injected:      make(map[string]int),
	}
	if opts.Recovery != RecoverIgnore {
		if err := m.recoverStale(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) recoverStale() error {
	ids, err := m.store.listIDs()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, id := range ids {
		meta, err := m.store.readMeta(id)
		if err != nil {
			m.reportStore("readMeta", id, err)
			continue
		}
		if meta.Status.Terminal() {
			continue
		}
		meta.Status = StatusFailed
		meta.FinishedAt = now
		meta.Error = "interrupted: process restarted before job finished"
		if err := m.store.writeMeta(meta); err != nil {
			return fmt.Errorf("job: recover %s: %w", id, err)
		}
		if err := m.store.appendEvent(meta, "recovered: marked failed after restart"); err != nil {
			m.reportStore("appendEvent", id, err)
		}
	}
	return nil
}

func (m *Manager) reportStore(op, jobID string, err error) {
	if err == nil || m.onStoreError == nil {
		return
	}
	m.onStoreError(op, jobID, err)
}

func (m *Manager) persistMeta(meta Meta) {
	if err := m.store.writeMeta(meta); err != nil {
		m.reportStore("writeMeta", meta.ID, err)
	}
}

func (m *Manager) persistEvent(meta Meta, msg string) {
	if err := m.store.appendEvent(meta, msg); err != nil {
		m.reportStore("appendEvent", meta.ID, err)
	}
}

// Spawn starts a job asynchronously. The returned Info reflects starting state.
//
// Concurrency: if MaxConcurrent slots are full, Spawn returns [ErrBusy]
// (jobs are not queued). Depth: if req.Depth >= MaxDepth, returns [ErrDepth].
func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (Info, error) {
	if err := req.validate(); err != nil {
		return Info{}, err
	}
	if req.Depth >= m.maxDepth {
		return Info{}, fmt.Errorf("%w: depth %d >= max %d", ErrDepth, req.Depth, m.maxDepth)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Info{}, ErrClosed
	}
	select {
	case m.slots <- struct{}{}:
	default:
		m.mu.Unlock()
		return Info{}, ErrBusy
	}
	m.mu.Unlock()

	id, err := newJobID()
	if err != nil {
		<-m.slots
		return Info{}, err
	}
	now := time.Now().UTC()
	meta := Meta{
		ID:          id,
		ParentID:    req.ParentID,
		ParentDepth: req.Depth,
		Role:        NormalizeRole(string(req.Role)),
		Prompt:      req.Prompt,
		Description: req.Description,
		WorkDir:     req.WorkDir,
		Status:      StatusStarting,
		CreatedAt:   now,
	}
	meta, err = m.store.create(meta)
	if err != nil {
		<-m.slots
		return Info{}, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, req.Timeout)
	}
	// Parent ctx cancel does not kill the job (jobs outlive a single tool call);
	// callers use Cancel. Still respect if Spawn itself is aborted before start.
	if err := ctx.Err(); err != nil {
		cancel()
		<-m.slots
		meta.Status = StatusCancelled
		meta.FinishedAt = time.Now().UTC()
		meta.Error = err.Error()
		m.persistMeta(meta)
		return Info{}, err
	}

	lj := &liveJob{
		meta:            meta,
		parentToolUseID: req.ParentToolUseID,
		cancel:          cancel,
		done:            make(chan struct{}),
		mailbox:         make(chan string, 8),
		runCtx:          runCtx,
	}
	m.mu.Lock()
	m.jobs[id] = lj
	m.snapshots[id] = Info{Meta: meta}
	m.mu.Unlock()
	m.emitProgress(Progress{JobID: id, Status: string(meta.Status), Time: now})

	go m.run(runCtx, lj)
	return Info{Meta: meta}, nil
}

func (m *Manager) run(ctx context.Context, lj *liveJob) {
	defer close(lj.done)
	defer lj.cancel()
	defer func() { <-m.slots }()

	meta := lj.meta
	meta.Status = StatusRunning
	meta.StartedAt = time.Now().UTC()
	m.persistMeta(meta)
	m.setLiveMeta(meta)
	m.persistEvent(meta, "running")
	m.emitProgress(Progress{JobID: meta.ID, Status: string(meta.Status), Time: meta.StartedAt})

	env := RunEnv{
		Job: meta,
		DrainSteering: func(final bool) []string {
			return m.drainSteering(lj, final)
		},
		AckSteering: func(count int) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if count > 0 && count <= lj.steeringPending {
				lj.steeringPending -= count
				m.injected[meta.ID] += count
			}
		},
		Log: func(message string) {
			m.persistEvent(meta, message)
		},
		WriteResult: func(summary string) error {
			err := m.store.writeResult(meta, summary)
			if err != nil {
				m.reportStore("writeResult", meta.ID, err)
			}
			return err
		},
		OnProgress: func(p Progress) {
			p.JobID = meta.ID
			if p.ParentToolUseID == "" {
				p.ParentToolUseID = lj.parentToolUseID
			}
			if p.Time.IsZero() {
				p.Time = time.Now().UTC()
			}
			// Accumulate usage delta when this progress carries usage info.
			if p.Usage != nil {
				m.mu.Lock()
				lj.meta.Usage.InputTokens += p.Usage.InputTokens
				lj.meta.Usage.OutputTokens += p.Usage.OutputTokens
				lj.meta.Usage.CacheRead += p.Usage.CacheRead
				lj.meta.Usage.CacheWrite += p.Usage.CacheWrite
				lj.meta.Usage.CostUSD += p.Usage.CostUSD
				m.snapshots[meta.ID] = Info{Meta: lj.meta}
				m.mu.Unlock()
			}
			m.emitProgress(p)
		},
	}

	summary, err := m.runner.Run(ctx, env)
	m.mu.Lock()
	lj.steeringClosed = true
	m.mu.Unlock()

	meta.FinishedAt = time.Now().UTC()

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		meta.Status = StatusTimedOut
		if err != nil {
			meta.Error = err.Error()
		} else {
			meta.Error = ctx.Err().Error()
		}
		m.persistEvent(meta, "timed_out: "+meta.Error)
	case errors.Is(ctx.Err(), context.Canceled):
		meta.Status = StatusCancelled
		if err != nil {
			meta.Error = err.Error()
		} else {
			meta.Error = ctx.Err().Error()
		}
		m.persistEvent(meta, "cancelled: "+meta.Error)
	case err != nil:
		meta.Status = StatusFailed
		meta.Error = err.Error()
		m.persistEvent(meta, "failed: "+meta.Error)
	default:
		meta.Status = StatusCompleted
		if summary != "" {
			if werr := m.store.writeResult(meta, summary); werr != nil {
				m.reportStore("writeResult", meta.ID, werr)
				meta.Status = StatusFailed
				meta.Error = "failed to write result.md: " + werr.Error()
				m.persistEvent(meta, "failed: "+meta.Error)
			} else {
				m.persistEvent(meta, "completed")
			}
		} else {
			m.persistEvent(meta, "completed")
		}
	}

	m.mu.Lock()
	meta.Usage = lj.meta.Usage
	m.mu.Unlock()
	m.persistMeta(meta)
	m.setLiveMeta(meta)

	m.mu.Lock()
	delete(m.jobs, meta.ID)
	m.mu.Unlock()
	m.emitProgress(Progress{JobID: meta.ID, Status: string(meta.Status), Time: meta.FinishedAt})
}

func (m *Manager) setLiveMeta(meta Meta) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lj, ok := m.jobs[meta.ID]; ok {
		// Status transitions carry an older copy of metadata, never newer usage.
		meta.Usage = lj.meta.Usage
		lj.meta = meta
	}
	m.snapshots[meta.ID] = Info{Meta: meta}
}

// MaxSteeringRounds bounds batches injected into one child run.
const MaxSteeringRounds = 5

// Send queues steering for a running job without blocking. Success means queued,
// not injected: cancellation or an engine error may still prevent delivery.
func (m *Manager) Send(jobID, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("%w: steering message must not be blank", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	lj, ok := m.jobs[jobID]
	if !ok || lj.meta.Status != StatusRunning || lj.steeringClosed || lj.runCtx.Err() != nil {
		return fmt.Errorf("%w: %s is not accepting steering", ErrNotRunning, jobID)
	}
	if lj.steeringRounds >= MaxSteeringRounds {
		return ErrSteeringLimit
	}
	select {
	case lj.mailbox <- msg:
		return nil
	default:
		return ErrMailboxFull
	}
}

func (m *Manager) drainSteering(lj *liveJob, final bool) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lj.steeringClosed || lj.runCtx.Err() != nil || lj.steeringRounds >= MaxSteeringRounds {
		return nil
	}
	var messages []string
	for len(lj.mailbox) > 0 {
		messages = append(messages, <-lj.mailbox)
	}
	if len(messages) > 0 {
		lj.steeringRounds++
		lj.steeringPending += len(messages)
	} else if final {
		lj.steeringClosed = true
	}
	return messages
}

// InjectedMessages returns acknowledged steering messages, including completed
// jobs. Counts are process-local; unknown jobs and jobs from prior runs return 0.
func (m *Manager) InjectedMessages(jobID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.injected[jobID]
}

// Get returns one job by id.
func (m *Manager) Get(ctx context.Context, id string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	return m.loadInfo(id)
}

func (m *Manager) loadInfo(id string) (Info, error) {
	m.mu.Lock()
	if lj, ok := m.jobs[id]; ok {
		meta := lj.meta
		m.mu.Unlock()
		return Info{Meta: meta}, nil
	}
	m.mu.Unlock()
	meta, err := m.store.readMeta(id)
	if err != nil {
		return Info{}, err
	}
	return Info{Meta: meta}, nil
}

// Wait blocks until the job reaches a terminal status or ctx is done.
//
// A Wait timeout (ctx deadline) does not cancel the job; use [Manager.Cancel].
func (m *Manager) Wait(ctx context.Context, id string) (WaitResult, error) {
	for {
		info, err := m.loadInfo(id)
		if err != nil {
			return WaitResult{}, err
		}
		if info.Status.Terminal() {
			summary, _ := m.store.readResult(info.Meta)
			return WaitResult{Info: info, Summary: summary}, nil
		}

		m.mu.Lock()
		lj, ok := m.jobs[id]
		var done <-chan struct{}
		if ok {
			done = lj.done
		}
		m.mu.Unlock()

		if !ok {
			info, err = m.loadInfo(id)
			if err != nil {
				return WaitResult{}, err
			}
			if info.Status.Terminal() {
				summary, _ := m.store.readResult(info.Meta)
				return WaitResult{Info: info, Summary: summary}, nil
			}
			return WaitResult{}, fmt.Errorf("%w: %s disappeared while non-terminal", ErrNotFound, id)
		}

		select {
		case <-ctx.Done():
			return WaitResult{}, fmt.Errorf("%w: %w", ErrWaitTimeout, ctx.Err())
		case <-done:
		}
	}
}

// Log returns the last limit events (0 = all).
func (m *Manager) Log(ctx context.Context, id string, limit int) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := m.loadInfo(id)
	if err != nil {
		return nil, err
	}
	return m.store.readEvents(info.Meta, limit)
}

// Cancel requests cancellation of a running/starting job.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	lj, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		info, err := m.loadInfo(id)
		if err != nil {
			return err
		}
		if info.Status.Terminal() {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrNotRunning, id)
	}
	lj.cancel()
	select {
	case <-lj.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close cancels all live jobs and waits for them to exit.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	lives := make([]*liveJob, 0, len(m.jobs))
	for _, lj := range m.jobs {
		lives = append(lives, lj)
		lj.cancel()
	}
	// Close subscriber channels under the lock so they cannot race a send in
	// emitProgress (which also holds m.mu). The closed flag makes Close safe
	// even if a subscriber's cancel func runs later.
	for _, s := range m.subs {
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
	}
	m.subs = nil
	m.mu.Unlock()

	for _, lj := range lives {
		select {
		case <-lj.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Subscribe receives live [Progress] events. The channel is buffered; slow
// consumers may miss events. Cancel closes the channel and unregisters.
func (m *Manager) Subscribe() (<-chan Progress, func()) {
	ch := make(chan Progress, 64)
	sub := &subscriber{ch: ch}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	m.subs = append(m.subs, sub)
	m.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			for i, s := range m.subs {
				if s == sub {
					m.subs = slices.Delete(m.subs, i, i+1)
					break
				}
			}
			// Idempotent vs Manager.Close: never close an already closed channel.
			if !sub.closed {
				sub.closed = true
				close(ch)
			}
		})
	}
	return ch, cancel
}

func (m *Manager) emitProgress(p Progress) {
	// Sends are non-blocking, so holding the lock is safe; it makes send and
	// channel close mutually exclusive (cancel/Close close under m.mu).
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.subs {
		select {
		case s.ch <- p:
		default:
			// drop if subscriber is slow
		}
	}
}

// ParentToolUseID returns the UI correlation id for a live job, if any.
func (m *Manager) ParentToolUseID(jobID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lj, ok := m.jobs[jobID]; ok {
		return lj.parentToolUseID
	}
	return ""
}

// MaxDepth exposes the configured nesting ceiling (for tool adapters).
func (m *Manager) MaxDepth() int { return m.maxDepth }

// MaxConcurrent exposes the concurrency ceiling.
func (m *Manager) MaxConcurrent() int { return m.maxConcurrent }

// LiveCount returns how many jobs are currently starting/running in-process.
func (m *Manager) LiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

// Snapshot returns in-memory snapshots of jobs spawned in this process, ordered
// by creation time then ID. An empty parentID includes all parents.
func (m *Manager) Snapshot(parentID string) []Info {
	m.mu.Lock()
	infos := make([]Info, 0, len(m.snapshots))
	for _, info := range m.snapshots {
		if parentID == "" || info.ParentID == parentID {
			infos = append(infos, info)
		}
	}
	m.mu.Unlock()
	slices.SortFunc(infos, func(a, b Info) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return infos
}

// Usage returns authoritative accumulated usage for a job spawned in this process,
// including completed jobs. Unknown jobs return zero usage.
func (m *Manager) Usage(jobID string) UsageDelta {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshots[jobID].Usage
}
