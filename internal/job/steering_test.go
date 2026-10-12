package job_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/job"
)

func steeringManager(t *testing.T) (*job.Manager, job.Info, job.RunEnv, chan struct{}) {
	t.Helper()
	ready := make(chan job.RunEnv, 1)
	finish := make(chan struct{})
	m := newMgr(t, job.RunnerFunc(func(ctx context.Context, env job.RunEnv) (string, error) {
		ready <- env
		select {
		case <-finish:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), job.Options{})
	info, err := m.Spawn(t.Context(), job.SpawnRequest{Prompt: "go"})
	require.NoError(t, err)
	return m, info, <-ready, finish
}

func TestSteeringMailboxBoundsAndAcknowledgement(t *testing.T) {
	m, info, env, finish := steeringManager(t)
	require.ErrorIs(t, m.Send(info.ID, " \n"), job.ErrInvalid)
	require.ErrorIs(t, m.Send("missing", "go"), job.ErrNotRunning)
	for round := range job.MaxSteeringRounds {
		var want []string
		for i := range 8 {
			text := fmt.Sprintf("%d/%d", round, i)
			want = append(want, text)
			require.NoError(t, m.Send(info.ID, text))
		}
		require.ErrorIs(t, m.Send(info.ID, "overflow"), job.ErrMailboxFull)
		require.Equal(t, want, env.DrainSteering(false))
		require.Equal(t, round*8, m.InjectedMessages(info.ID))
		env.AckSteering(9) // Invalid acknowledgements must not inflate the count.
		require.Equal(t, round*8, m.InjectedMessages(info.ID))
		env.AckSteering(8)
	}
	require.ErrorIs(t, m.Send(info.ID, "too late"), job.ErrSteeringLimit)
	close(finish)
	_, err := m.Wait(t.Context(), info.ID)
	require.NoError(t, err)
	require.Equal(t, 40, m.InjectedMessages(info.ID))
	require.ErrorIs(t, m.Send(info.ID, "finished"), job.ErrNotRunning)
	require.Zero(t, m.InjectedMessages("missing"))
}

func TestSteeringFinalDrainSerializesWithSend(t *testing.T) {
	m, info, env, finish := steeringManager(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var sendErr error
	var messages []string
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		sendErr = m.Send(info.ID, "correction")
	}()
	go func() {
		defer wg.Done()
		<-start
		messages = env.DrainSteering(true)
	}()
	close(start)
	wg.Wait()
	if sendErr == nil {
		require.Equal(t, []string{"correction"}, messages)
		env.AckSteering(1)
		require.Empty(t, env.DrainSteering(true))
	} else {
		require.ErrorIs(t, sendErr, job.ErrNotRunning)
		require.Empty(t, messages)
	}
	require.ErrorIs(t, m.Send(info.ID, "after final drain"), job.ErrNotRunning)
	close(finish)
	_, err := m.Wait(t.Context(), info.ID)
	require.NoError(t, err)
	require.Equal(t, len(messages), m.InjectedMessages(info.ID))
}

func TestSteeringCancellationAndClose(t *testing.T) {
	m, info, env, _ := steeringManager(t)
	require.NoError(t, m.Send(info.ID, "pending"))
	require.NoError(t, m.Cancel(t.Context(), info.ID))
	require.Empty(t, env.DrainSteering(false))
	require.Zero(t, m.InjectedMessages(info.ID))
	require.ErrorIs(t, m.Send(info.ID, "cancelled"), job.ErrNotRunning)
	require.NoError(t, m.Close(t.Context()))
	require.ErrorIs(t, m.Send(info.ID, "closed"), job.ErrClosed)
}
