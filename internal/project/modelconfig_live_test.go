package project

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/llm"
	llmclient "github.com/pulseaiclub/phi/internal/llm/client"
	"github.com/pulseaiclub/phi/internal/project/model"
)

func TestLiveGatewayModelConfig(t *testing.T) {
	endpoint, key := os.Getenv("PHI_TEST_GATEWAY_URL"), os.Getenv("PHI_TEST_GATEWAY_KEY")
	name := os.Getenv("PHI_TEST_GATEWAY_MODEL")
	if endpoint == "" || key == "" || name == "" {
		t.Skip(
			"set PHI_TEST_GATEWAY_URL, PHI_TEST_GATEWAY_KEY and PHI_TEST_GATEWAY_MODEL to test an OpenAI-compatible gateway",
		)
	}
	cfg, err := ResolveModelDoc(ModelDoc{Name: name, API: "OpenAI", BaseURL: endpoint, APIKey: key})
	require.NoError(t, err)
	client := llmclient.NewClient(cfg, model.HooksForConfig(cfg), nil, "")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var answer *llm.Message
	for event, err := range client.Stream(ctx, []llm.Message{{Role: llm.RoleUser, Content: "Reply with exactly OK."}}) {
		require.NoError(t, err)
		if event.Final != nil {
			answer = event.Final
		}
	}
	require.NotNil(t, answer)
	require.NotEmpty(t, strings.TrimSpace(answer.Content))
	result, err := client.Compact(ctx, llm.CompactRequest{
		Prompt:    "Summarize in one short sentence: The button is blue. The next step is adding a label.",
		MaxTokens: 1024,
	})
	require.NoError(t, err)
	assert.False(t, result.Truncated)
	require.NotEmpty(t, strings.TrimSpace(result.Text))
}
