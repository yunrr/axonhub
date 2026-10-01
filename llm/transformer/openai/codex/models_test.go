package codex

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupportsModelCatalog(t *testing.T) {
	for _, tt := range []struct {
		name    string
		baseURL string
		want    bool
	}{
		{"empty defaults to official", "", true},
		{"openai api url", "https://api.openai.com/v1", true},
		{"official backend", "https://chatgpt.com/backend-api/codex", true},
		{"official backend with fragment", "https://chatgpt.com/backend-api/codex#", true},
		{"official backend with trailing slash", "https://chatgpt.com/backend-api/codex/", true},
		{"host is case insensitive", "https://CHATGPT.com/backend-api/codex", true},
		{"relay domain", "https://relay.example/v1", false},
		{"wrong path", "https://chatgpt.com/backend-api/other", false},
		{"insecure scheme", "http://chatgpt.com/backend-api/codex", false},
		{"subdomain is not official", "https://evil.chatgpt.com/backend-api/codex", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, SupportsModelCatalog(tt.baseURL))
		})
	}
}

func TestParseModelCatalog(t *testing.T) {
	body := []byte(`{"models":[
		{"slug":"hidden-low","visibility":"hide","priority":0},
		{"slug":"second","visibility":"list","priority":2},
		{"slug":"first","visibility":"list","priority":1},
		{"slug":"","visibility":"list","priority":3}
	]}`)
	models, err := ParseModelCatalog(body)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, models)

	_, err = ParseModelCatalog([]byte(`{`))
	require.Error(t, err)

	_, err = ParseModelCatalog([]byte(`{"models":[{"slug":"x","visibility":"hide"}]}`))
	require.Error(t, err)
}
