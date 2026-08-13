package logger

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeLogArgsRedactsNestedSecrets(t *testing.T) {
	clean := sanitizeLogArgs([]any{
		"params", map[string]any{
			"hf_token": "hf_secret",
			"nested": map[string]any{
				"api_key": "sk_secret",
			},
		},
		"url", "https://example.com/callback?token=secret&job=123",
	})

	params := clean[1].(map[string]any)
	assert.Equal(t, "[REDACTED]", params["hf_token"])
	assert.Equal(t, "[REDACTED]", params["nested"].(map[string]any)["api_key"])
	assert.NotContains(t, clean[3], "secret")
	assert.NotContains(t, clean[3], "123")
	assert.Equal(t, "https://example.com/callback?redacted", clean[3])
}

func TestRedactURLCoversAuthorizationValues(t *testing.T) {
	redacted := RedactURL("/auth/cli/callback?code=abc&access_token=def&state=kept")
	assert.NotContains(t, redacted, "abc")
	assert.NotContains(t, redacted, "def")
	assert.NotContains(t, redacted, "kept")
	assert.Equal(t, "/auth/cli/callback?redacted", redacted)
}
