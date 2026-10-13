package agent_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/agent"
	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/llm"
)

// TestEngineRunnerChildUsesItsOwnWorkdir pins PP-03 end to end: the request a
// sub-agent sends must carry the instructions and cwd of its own workdir, not
// the ones of the process that spawned it.
func TestEngineRunnerChildUsesItsOwnWorkdir(t *testing.T) {
	var mu sync.Mutex
	var bodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", jsonMarshalDelta("recon done"))
		_, _ = fmt.Fprint(
			w,
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n",
		)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	workdir := t.TempDir()
	require.NoError(t,
		os.WriteFile(filepath.Join(workdir, "AGENTS.md"), []byte("child-workdir-rule"), 0o644))

	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: agent.EngineRunner{
			Model:     llm.ModelConfig{Name: "fake", BaseURL: srv.URL, APIKey: "x"},
			MaxRounds: 4,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	info, err := mgr.Spawn(t.Context(), job.SpawnRequest{Prompt: "recon", WorkDir: workdir})
	require.NoError(t, err)
	_, err = mgr.Wait(t.Context(), info.ID)
	require.NoError(t, err)

	mu.Lock()
	joined := strings.Join(bodies, "\n")
	mu.Unlock()
	require.NotEmpty(t, joined, "child engine should have issued a request")

	assert.Contains(t, joined, "child-workdir-rule", "child must load the AGENTS.md of its workdir")
	assert.Contains(t, joined, "Current directory: "+workdir)

	processCwd, err := os.Getwd()
	require.NoError(t, err)
	assert.NotContains(t, joined, "Current directory: "+processCwd,
		"child must not report the parent process cwd")
}
