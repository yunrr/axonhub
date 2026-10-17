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
	require.Equal(t, []string{"first", "second", "gpt-image-2"}, models)

	_, err = ParseModelCatalog([]byte(`{`))
	require.Error(t, err)

	_, err = ParseModelCatalog([]byte(`{"models":[{"slug":"x","visibility":"hide"}]}`))
	require.Error(t, err)
}

func TestCodexModelsIncludeImageModel(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want []string
	}{
		{"missing", `{"models":[{"slug":"second","visibility":"list","priority":2},{"slug":"first","visibility":"list","priority":1}]}`, []string{"first", "second", "gpt-image-2"}},
		{"existing", `{"models":[{"slug":"first","visibility":"list","priority":1},{"slug":"gpt-image-2","visibility":"list","priority":2},{"slug":"last","visibility":"list","priority":3}]}`, []string{"first", "gpt-image-2", "last"}},
		{"duplicate image", `{"models":[{"slug":"gpt-image-2","visibility":"list","priority":1},{"slug":"first","visibility":"list","priority":2},{"slug":"gpt-image-2","visibility":"list","priority":3}]}`, []string{"gpt-image-2", "first"}},
		{"stable order", `{"models":[{"slug":"second","visibility":"list","priority":1},{"slug":"first","visibility":"list","priority":1},{"slug":"second","visibility":"list","priority":2}]}`, []string{"second", "first", "second", "gpt-image-2"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given a nonempty visible upstream catalog.
			body := []byte(tt.body)
			// When the catalog is parsed.
			models, err := ParseModelCatalog(body)
			// Then one image model is included without reordering other models.
			require.NoError(t, err)
			require.Equal(t, tt.want, models)
		})
	}

	t.Run("fallback", func(t *testing.T) {
		// Given the static fallback catalog, when it is requested.
		models := DefaultModels()
		// Then its original order is retained with the image model appended once.
		require.Equal(t, []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol", "codex-auto-review", "gpt-image-2"}, models)
	})

	for _, body := range []string{
		`{"models":[]}`,
		`{"models":[{"slug":"gpt-image-2","visibility":"hide"}]}`,
		`{"models":[{"slug":" ","visibility":"list"}]}`,
	} {
		t.Run("empty visible catalog "+body, func(t *testing.T) {
			// Given a catalog with no valid visible models, when it is parsed.
			models, err := ParseModelCatalog([]byte(body))
			// Then the original validation still rejects it before supplementation.
			require.EqualError(t, err, "Codex catalog has no visible models")
			require.Nil(t, models)
		})
	}
}

func TestThirdPartyModelCatalogKeepsListedSlugsRegardlessOfVisibility(t *testing.T) {
	models, err := ParseThirdPartyModelCatalog([]byte(`{"models":[{"slug":"visible","visibility":"list","priority":1},{"slug":"hidden","visibility":"hide","priority":2}]}`))

	require.NoError(t, err)
	require.Equal(t, []string{"visible", "hidden"}, models)
}
