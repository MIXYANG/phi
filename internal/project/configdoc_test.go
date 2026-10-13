package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseConfigFile(path string) (*Config, error) {
	return parseConfigFileWithEnv(path, false)
}

const configDocFixture = `models:
  - name: model-a
    api_key: key-a
    base_url: https://a.example/v1
    context_window: 1000
    image_enabled: true
    default: true
  - name: model-b
    api_key: key-b
    base_url: https://b.example/v1
permissions:
  mode: readonly
  dangerously_allow_all: true
  bash:
    default: ask
    allow:
      - "^git "
agents:
  enabled: false
`

func TestConfigDocRoundTripThroughLoader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(configDocFixture), 0o600))

	doc, err := ReadConfigDoc(path)
	require.NoError(t, err)
	require.Len(t, doc.Models, 2)
	assert.Equal(t, "model-a", doc.Models[0].Name)
	assert.True(t, doc.Models[0].Default)
	require.NotNil(t, doc.Models[0].ImageEnabled)
	assert.True(t, *doc.Models[0].ImageEnabled)
	require.NotNil(t, doc.Permissions)
	require.NotNil(t, doc.Permissions.Bash)
	require.NotNil(t, doc.Permissions.Bash.Allow)
	assert.Equal(t, StringList{"^git "}, *doc.Permissions.Bash.Allow)
	require.NotNil(t, doc.Agents)
	require.NotNil(t, doc.Agents.Enabled)
	assert.False(t, *doc.Agents.Enabled)

	// An edit then a save must load back with the same values the runtime uses.
	doc.Models[0].APIKey = "new-key"
	doc.Models = doc.Models[:1]
	require.NoError(t, doc.Save(path))

	loaded, err := parseConfigFile(path)
	require.NoError(t, err)
	require.Len(t, loaded.Models, 1)
	assert.Equal(t, "model-a", loaded.Model().Name)
	assert.Equal(t, "new-key", loaded.Model().APIKey)
	assert.Equal(t, 1000, loaded.Model().ContextWindow)
	assert.True(t, loaded.Model().ImageEnabled)
	assert.Equal(t, "readonly", string(loaded.Permissions.Mode))
	assert.True(t, loaded.Permissions.DangerouslyAllowAll)
	assert.Equal(t, []string{"^git "}, loaded.Permissions.BashAllow)
	assert.False(t, loaded.Agents.Enabled)

	// The previous file is kept as a backup.
	backup, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Contains(t, string(backup), "model-b")
}

func TestInvalidRequestParametersFailConfigLoad(t *testing.T) {
	for _, tt := range []struct{ name, api, params, want string }{
		{"sdk wrapper", "OpenAI", "{extra_body: {thinking: {type: enabled}}}", "SDK wrapper"},
		{"reserved", "OpenAI", "{messages: null}", "messages is managed by phi"},
		{"protocol", "Unknown", "{thinking: null}", "unsupported API"},
		{"preset protocol", "", "{input: null}", "input is managed by phi"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data := "models:\n  - name: gpt-6-astra\n    api_key: fake-key\n    api: " + tt.api + "\n    request_parameters: " + tt.params + "\n"
			require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
			_, err := parseConfigFile(path)
			require.ErrorContains(t, err, tt.want)
			doc, err := ReadConfigDoc(path)
			require.NoError(t, err)
			require.ErrorContains(t, doc.Validate(), tt.want)
		})
	}
}

func TestConfigDocKeepsAbsentKeysAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("models:\n  - name: m\n    api_key: k\n"), 0o600))

	doc, err := ReadConfigDoc(path)
	require.NoError(t, err)
	require.NoError(t, doc.Save(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	// Keys the editor never touched must not appear: their absence is what keeps
	// the loader defaults (permissions policy, agents on) in force.
	for _, key := range []string{"skill_path", "permissions", "agents", "image_enabled", "context_window"} {
		assert.NotContains(t, string(data), key)
	}
	assert.Contains(t, string(data), "name: m")
}

func TestConfigDocMissingFileIsEmpty(t *testing.T) {
	doc, err := ReadConfigDoc(filepath.Join(t.TempDir(), "nope.yaml"))
	require.NoError(t, err)
	assert.Empty(t, doc.Models)
	assert.Nil(t, doc.Permissions)
}

func TestConfigDocMalformedFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("models: [\n"), 0o600))
	_, err := ReadConfigDoc(path)
	require.Error(t, err)
}

func TestConfigDocValidate(t *testing.T) {
	cases := []struct {
		name    string
		doc     ConfigDoc
		wantErr string
	}{
		{"no models", ConfigDoc{}, "at least one model"},
		{"unnamed model", ConfigDoc{Models: []ModelDoc{{}}}, "has no name"},
		{"duplicate names", ConfigDoc{Models: []ModelDoc{{Name: "m", APIKey: "k"}, {Name: "m"}}}, "duplicate"},
		{
			"two defaults",
			ConfigDoc{Models: []ModelDoc{
				{Name: "a", APIKey: "k", Default: true},
				{Name: "b", APIKey: "k", Default: true},
			}},
			"only one model",
		},
		{
			"default without key",
			ConfigDoc{Models: []ModelDoc{{Name: "a", Default: true}}},
			"missing api_key",
		},
		{"no explicit default is fine", ConfigDoc{Models: []ModelDoc{{Name: "a", APIKey: "k"}}}, ""},
		{
			"first model is the implicit default",
			ConfigDoc{Models: []ModelDoc{{Name: "a"}, {Name: "b", APIKey: "k"}}},
			"used by default",
		},
		{
			"keyless non-default model is fine",
			ConfigDoc{Models: []ModelDoc{{Name: "a", APIKey: "k"}, {Name: "b"}}},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.doc.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
