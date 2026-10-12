package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/llm"
	"github.com/pulseaiclub/phi/internal/permission"
	"github.com/pulseaiclub/phi/internal/tools"
)

func TestLoopSteeringAfterToolResultsPreservesGate(t *testing.T) {
	for _, denied := range []bool{false, true} {
		name := "allowed"
		if denied {
			name = "denied"
		}
		t.Run(name, func(t *testing.T) {
			server, requests := fakeToolSequenceServer(1)
			defer server.Close()
			var runs atomic.Int32
			engine := newRoundTestEngine(t, server.URL, &runs, 2)
			if denied {
				engine.SetPermission(fixedGate{dec: permission.Deny, reason: "blocked"}, nil)
			}
			rounds, acknowledged := 0, 0
			opts := LoopOpts{
				OnRoundEnd: func() []string {
					rounds++
					if rounds != 1 {
						return nil
					}
					messages := engine.Session().BuildContext()
					require.Equal(t, llm.RoleTool, messages[len(messages)-1].Role)
					if denied {
						require.Equal(t, "blocked", messages[len(messages)-1].Content)
					}
					return []string{"first correction", "second correction"}
				},
				OnSteeringInjected: func(n int) { acknowledged += n },
			}
			for _, err := range engine.Loop(t.Context(), "go", opts) {
				require.NoError(t, err)
			}
			require.EqualValues(t, 2, requests.Load())
			require.Equal(t, 2, acknowledged)
			wantRuns := int32(1)
			if denied {
				wantRuns = 0
			}
			require.Equal(t, wantRuns, runs.Load())
			messages := engine.Session().BuildContext()
			require.Equal(t, "first correction", messages[len(messages)-3].Content)
			require.Equal(t, "second correction", messages[len(messages)-2].Content)
		})
	}
}

func TestLoopSteeringContinuesTextAndFinalDrain(t *testing.T) {
	server, requests := fakeToolSequenceServer(0)
	defer server.Close()
	var runs atomic.Int32
	engine := newRoundTestEngine(t, server.URL, &runs, 1)
	rounds, stops, acknowledged := 0, 0, 0
	for _, err := range engine.Loop(t.Context(), "go", LoopOpts{
		OnRoundEnd: func() []string {
			rounds++
			if rounds == 1 {
				return []string{"change direction"}
			}
			return nil
		},
		OnStopping: func() []string {
			stops++
			if stops == 1 {
				return []string{"last minute correction"}
			}
			return nil
		},
		OnSteeringInjected: func(n int) { acknowledged += n },
	}) {
		require.NoError(t, err)
	}
	require.EqualValues(t, 3, requests.Load())
	require.Equal(t, 2, acknowledged)
	require.Equal(t, 2, stops)
}

func TestEngineRunnerSteeringLimit(t *testing.T) {
	server, requests := fakeToolSequenceServer(0)
	defer server.Close()
	runner := EngineRunner{
		Model: llm.ModelConfig{Name: "fake", BaseURL: server.URL, APIKey: "x"},
		Gate:  permission.AllowAll{}, Tools: []tools.Tool{},
	}
	drained, acknowledged := 0, 0
	_, err := runner.Run(t.Context(), job.RunEnv{
		Job:           job.Meta{Dir: t.TempDir(), WorkDir: t.TempDir(), Prompt: "go"},
		Log:           func(string) {},
		DrainSteering: func(bool) []string { drained++; return []string{"one", "two"} },
		AckSteering:   func(n int) { acknowledged += n },
	})
	require.NoError(t, err)
	require.Equal(t, job.MaxSteeringRounds, drained)
	require.Equal(t, 10, acknowledged)
	require.EqualValues(t, 6, requests.Load())
}

func TestLoopSteeringDoesNotResetToolBudget(t *testing.T) {
	server, requests := fakeToolSequenceServer(-1)
	defer server.Close()
	var runs atomic.Int32
	engine := newRoundTestEngine(t, server.URL, &runs, 1)
	acknowledged := 0
	var loopErr error
	for _, err := range engine.Loop(t.Context(), "go", LoopOpts{
		OnRoundEnd:         func() []string { return []string{"continue"} },
		OnSteeringInjected: func(n int) { acknowledged += n },
	}) {
		if err != nil {
			loopErr = err
		}
	}
	require.ErrorIs(t, loopErr, ErrMaxRounds)
	require.EqualValues(t, 1, runs.Load())
	require.EqualValues(t, 2, requests.Load())
	require.Equal(t, 1, acknowledged)
}

func TestLoopCancelledDoesNotDrainSteering(t *testing.T) {
	server, _ := fakeToolSequenceServer(0)
	defer server.Close()
	var runs atomic.Int32
	engine := newRoundTestEngine(t, server.URL, &runs, 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	for _, err := range engine.Loop(ctx, "go", LoopOpts{OnRoundEnd: func() []string {
		calls++
		return []string{"unused"}
	}}) {
		require.NoError(t, err)
	}
	require.Zero(t, calls)
}
