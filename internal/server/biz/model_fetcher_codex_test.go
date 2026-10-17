package biz

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent/channel"
	"github.com/looplj/axonhub/internal/ent/enttest"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

func TestModelFetcher_CodexCatalog(t *testing.T) {
	for _, tt := range []struct {
		name     string
		status   int
		body     string
		fallback bool
	}{
		{"visible catalog", 200, `{"models":[{"slug":"new-sol","visibility":"list","priority":2,"supported_in_api":false},{"slug":"hidden","visibility":"hide","priority":0},{"slug":"first-astra","visibility":"list","priority":1},{"slug":"new-sol","visibility":"list","priority":3}]}`, false},
		{"unauthorized", 401, `{}`, true},
		{"malformed", 200, `{`, true},
		{"empty", 200, `{"models":[]}`, true},
		{"hidden only", 200, `{"models":[{"slug":"hidden","visibility":"hide"}]}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := httpclient.NewHttpClientWithClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "https://chatgpt.com/backend-api/codex/models", req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
				require.NotEmpty(t, req.URL.Query().Get("client_version"))
				require.Equal(t, "Bearer synthetic-token", req.Header.Get("Authorization"))
				require.Equal(t, req.URL.Query().Get("client_version"), req.Header.Get("Version"))
				require.NotEmpty(t, req.Header.Get(codex.BetaFeaturesHeader))
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})})
			fetcher := NewModelFetcher(client, nil)
			key := `{"access_token":"synthetic-token","token_type":"bearer","expires_at":"2099-01-01T00:00:00Z"}`
			result, err := fetcher.FetchModels(t.Context(), FetchModelsInput{
				ChannelType: channel.TypeCodex.String(), BaseURL: "https://chatgpt.com/backend-api/codex#", APIKey: &key,
			})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, tt.fallback, result.Fallback)
			require.Nil(t, result.Error)
			imageModelCount := 0
			for _, model := range result.Models {
				if model.ID == "gpt-image-2" {
					imageModelCount++
				}
			}
			require.Equal(t, 1, imageModelCount)
			if tt.fallback {
				require.Len(t, result.Models, len(codex.DefaultModels()))
			} else {
				require.Equal(t, []ModelIdentify{{ID: "first-astra"}, {ID: "new-sol"}, {ID: "gpt-image-2"}}, result.Models)
			}
		})
	}
}

func TestModelFetcher_CodexThirdPartyCatalog_usesSlugModels(t *testing.T) {
	// Given
	var requestURL string
	var authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL = r.URL.String()
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"third-party-model","visibility":"list","priority":1}]}`))
	}))
	defer server.Close()

	key := "synthetic-api-key"
	fetcher := NewModelFetcher(httpclient.NewHttpClientWithClient(server.Client()), nil)

	// When
	result, err := fetcher.FetchModels(t.Context(), FetchModelsInput{
		ChannelType: channel.TypeCodex.String(),
		BaseURL:     server.URL + "/custom",
		APIKey:      &key,
	})

	// Then
	require.NoError(t, err)
	require.Nil(t, result.Error)
	require.False(t, result.Fallback)
	require.Equal(t, "/custom/models", strings.Split(requestURL, "?")[0])
	require.NotEmpty(t, requestURL)
	require.NotEmpty(t, strings.Split(requestURL, "client_version=")[1])
	require.Equal(t, "Bearer synthetic-api-key", authorization)
	require.Equal(t, []ModelIdentify{{ID: "third-party-model"}}, result.Models)
}

func TestModelFetcher_CodexThirdPartyCatalog_NormalizesSavedWebSocketURL(t *testing.T) {
	var requestURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL = r.URL.String()
		_, _ = w.Write([]byte(`{"models":[{"slug":"saved-wss-model","visibility":"list"}]}`))
	}))
	defer server.Close()

	key := "synthetic-api-key"
	baseURL := "wss://" + strings.TrimPrefix(server.URL, "https://") + "/custom"
	result, err := NewModelFetcher(httpclient.NewHttpClientWithClient(server.Client()), nil).FetchModels(t.Context(), FetchModelsInput{
		ChannelType: channel.TypeCodex.String(),
		BaseURL:     baseURL,
		APIKey:      &key,
	})

	require.NoError(t, err)
	require.Nil(t, result.Error)
	require.Equal(t, "/custom/models", strings.Split(requestURL, "?")[0])
	require.Equal(t, []ModelIdentify{{ID: "saved-wss-model"}}, result.Models)
}

func TestModelFetcher_CodexAPIKeyWithOfficialBaseURL_usesCatalog(t *testing.T) {
	client := httpclient.NewHttpClientWithClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "https://chatgpt.com/backend-api/codex/models", req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
		require.NotEmpty(t, req.URL.Query().Get("client_version"))
		require.Equal(t, "Bearer synthetic-api-key", req.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"official-url-api-key-model","visibility":"list"}]}`)),
		}, nil
	})})
	key := "synthetic-api-key"

	result, err := NewModelFetcher(client, nil).FetchModels(t.Context(), FetchModelsInput{
		ChannelType: channel.TypeCodex.String(),
		BaseURL:     "https://chatgpt.com/backend-api/codex#",
		APIKey:      &key,
	})

	require.NoError(t, err)
	require.Nil(t, result.Error)
	require.False(t, result.Fallback)
	require.Equal(t, []ModelIdentify{{ID: "official-url-api-key-model"}}, result.Models)
}

func TestModelFetcher_CodexThirdPartyCatalogRejectsHTTP(t *testing.T) {
	key := "synthetic-api-key"
	result, err := NewModelFetcher(httpclient.NewHttpClient(), nil).FetchModels(t.Context(), FetchModelsInput{
		ChannelType: channel.TypeCodex.String(),
		BaseURL:     "http://relay.example/backend-api/codex",
		APIKey:      &key,
	})

	require.NoError(t, err)
	require.Equal(t, "failed to prepare Codex models request: Codex catalog URL must use HTTPS", *result.Error)
	require.Empty(t, result.Models)
	require.False(t, result.Fallback)
}

func TestModelFetcher_CodexThirdPartyCatalogRejectsRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/redirected", http.StatusFound)
	}))
	defer server.Close()

	key := "synthetic-api-key"
	result, err := NewModelFetcher(httpclient.NewHttpClientWithClient(server.Client()), nil).FetchModels(t.Context(), FetchModelsInput{
		ChannelType: channel.TypeCodex.String(),
		BaseURL:     server.URL + "/backend-api/codex",
		APIKey:      &key,
	})

	require.NoError(t, err)
	require.NotNil(t, result.Error)
	require.Contains(t, *result.Error, "refusing redirect for credential-bearing request")
	require.Empty(t, result.Models)
	require.False(t, result.Fallback)
}

func TestModelFetcher_CodexThirdPartyCatalog_returnsErrorsWithoutFallback(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{}`},
		{name: "malformed", status: http.StatusOK, body: `{`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			var requestURL string
			var authorization string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestURL = r.URL.String()
				authorization = r.Header.Get("Authorization")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			key := "synthetic-api-key"

			// When
			result, err := NewModelFetcher(httpclient.NewHttpClientWithClient(server.Client()), nil).FetchModels(t.Context(), FetchModelsInput{
				ChannelType: channel.TypeCodex.String(),
				BaseURL:     server.URL + "/custom",
				APIKey:      &key,
			})

			// Then
			require.NoError(t, err)
			require.NotNil(t, result.Error)
			require.False(t, result.Fallback)
			require.Empty(t, result.Models)
			require.Equal(t, "/custom/models", strings.Split(requestURL, "?")[0])
			require.NotEmpty(t, strings.Split(requestURL, "client_version=")[1])
			require.Equal(t, "Bearer synthetic-api-key", authorization)
		})
	}
}

func TestModelFetcher_CodexStoredCredentialReplacement(t *testing.T) {
	for _, mode := range []string{"replace-oauth", "replace-api-key", "mismatch", "stored-api-key", "proxy"} {
		t.Run(mode, func(t *testing.T) {
			ctx := authz.WithSystemBypass(t.Context(), "test-model-fetch")
			db := enttest.NewEntClient(t, "sqlite3", "file:"+url.QueryEscape(t.Name())+"?mode=memory&_fk=0")
			defer db.Close()
			credentials := objects.ChannelCredentials{OAuth: &objects.OAuthCredentials{
				AccessToken: "stored-token", ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
			}}
			if mode == "stored-api-key" {
				credentials = objects.ChannelCredentials{APIKey: "stored-key"}
			}
			settings := &objects.ChannelSettings{HeaderOverrideOperations: []objects.OverrideOperation{
				{Op: "set", Path: "X-Catalog-Test", Value: "preserved"},
			}}
			proxyCalls := 0
			if mode == "proxy" {
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					proxyCalls++
					require.Equal(t, http.MethodConnect, req.Method)
					require.Equal(t, "chatgpt.com:443", req.Host)
					w.WriteHeader(http.StatusBadGateway)
				}))
				defer proxy.Close()
				settings.Proxy = &httpclient.ProxyConfig{Type: httpclient.ProxyTypeURL, URL: proxy.URL}
			}
			ch, err := db.Channel.Create().SetName("stored-codex").SetType(channel.TypeCodex).
				SetBaseURL("https://chatgpt.com/backend-api/codex#").SetCredentials(credentials).
				SetSettings(settings).SetSupportedModels([]string{"existing"}).SetDefaultTestModel("existing").Save(ctx)
			require.NoError(t, err)
			calls := 0
			client := httpclient.NewHttpClientWithClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "preserved", req.Header.Get("X-Catalog-Test"))
				body := `{"models":[{"slug":"new-model","visibility":"list"}]}`
				if mode == "replace-api-key" {
					require.Equal(t, "https://relay.example/v1/models", req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
					require.NotEmpty(t, req.URL.Query().Get("client_version"))
					require.Equal(t, "Bearer replacement-key", req.Header.Get("Authorization"))
				} else {
					require.Equal(t, "Bearer replacement-token", req.Header.Get("Authorization"))
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			key := `{"access_token":"replacement-token","expires_at":"2099-01-01T00:00:00Z"}`
			input := FetchModelsInput{ChannelID: &ch.ID, ChannelType: ch.Type.String(), BaseURL: ch.BaseURL, APIKey: &key}
			if mode == "replace-api-key" || mode == "mismatch" {
				input.BaseURL = "https://relay.example/v1"
				key = "replacement-key"
			}
			if mode == "mismatch" {
				input.APIKey = nil
			}
			fetcher := NewModelFetcher(client, &ChannelService{AbstractService: &AbstractService{db: db}, httpClient: client})
			result, err := fetcher.FetchModels(ctx, input)
			require.NoError(t, err)
			switch mode {
			case "mismatch":
				require.NotNil(t, result.Error)
				require.Zero(t, calls)
			case "proxy":
				require.Equal(t, 1, proxyCalls)
				require.Zero(t, calls)
				require.True(t, result.Fallback)
			default:
				require.Nil(t, result.Error)
				require.Equal(t, 1, calls)
				require.False(t, result.Fallback)
				want := []ModelIdentify{{ID: "new-model"}}
				if mode != "replace-api-key" {
					want = append(want, ModelIdentify{ID: "gpt-image-2"})
				}
				require.Equal(t, want, result.Models)
			}
			saved, err := db.Channel.Get(ctx, ch.ID)
			require.NoError(t, err)
			require.Equal(t, credentials, saved.Credentials)
		})
	}
}

func TestModelFetcher_CodexCatalogNamedOAuthEntries(t *testing.T) {
	for _, tt := range []struct {
		name            string
		expiresAt       time.Time
		refreshResponse string
		wantToken       string
	}{
		{"valid entry token", time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "", "entry-token"},
		{"expired entry token is refreshed and persisted", time.Now().Add(-time.Hour),
			`{"access_token":"refreshed-token","refresh_token":"rotated-token","token_type":"bearer","expires_in":3600}`,
			"refreshed-token"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := authz.WithSystemBypass(t.Context(), "test-model-fetch")
			db := enttest.NewEntClient(t, "sqlite3", "file:"+url.QueryEscape(t.Name())+"?mode=memory&_fk=0")
			defer db.Close()

			credentials := objects.ChannelCredentials{OAuths: []objects.NamedOAuthCredentials{{
				ID:   "sub-1",
				Name: "primary",
				Credentials: &objects.OAuthCredentials{
					AccessToken:  "entry-token",
					RefreshToken: "entry-refresh",
					ExpiresAt:    tt.expiresAt,
				},
			}}}
			ch, err := db.Channel.Create().SetName("named-entry-codex").SetType(channel.TypeCodex).
				SetBaseURL("https://chatgpt.com/backend-api/codex#").SetCredentials(credentials).
				SetSupportedModels([]string{"existing"}).SetDefaultTestModel("existing").Save(ctx)
			require.NoError(t, err)

			calls := 0
			client := httpclient.NewHttpClientWithClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host == "auth.openai.com" {
					require.Equal(t, "https://auth.openai.com/oauth/token", req.URL.String())
					require.Equal(t, http.MethodPost, req.Method)
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.refreshResponse))}, nil
				}
				require.Equal(t, "https://chatgpt.com/backend-api/codex/models", req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
				require.NotEmpty(t, req.URL.Query().Get("client_version"))
				require.Equal(t, "Bearer "+tt.wantToken, req.Header.Get("Authorization"))
				body := `{"models":[{"slug":"entry-model","visibility":"list"}]}`
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			fetcher := NewModelFetcher(client, &ChannelService{AbstractService: &AbstractService{db: db}, httpClient: client})
			result, err := fetcher.FetchModels(ctx, FetchModelsInput{
				ChannelType: channel.TypeCodex.String(), BaseURL: ch.BaseURL, ChannelID: &ch.ID,
			})
			require.NoError(t, err)
			require.Nil(t, result.Error)
			require.False(t, result.Fallback)
			require.Equal(t, []ModelIdentify{{ID: "entry-model"}, {ID: "gpt-image-2"}}, result.Models)

			if tt.refreshResponse == "" {
				require.Equal(t, 1, calls)
				return
			}
			// The rotated refresh token must be persisted back into the named entry.
			require.Equal(t, 2, calls)
			saved, err := db.Channel.Get(ctx, ch.ID)
			require.NoError(t, err)
			require.Len(t, saved.Credentials.OAuths, 1)
			require.Equal(t, "sub-1", saved.Credentials.OAuths[0].ID)
			require.Equal(t, "primary", saved.Credentials.OAuths[0].Name)
			require.Equal(t, "refreshed-token", saved.Credentials.OAuths[0].Credentials.AccessToken)
			require.Equal(t, "rotated-token", saved.Credentials.OAuths[0].Credentials.RefreshToken)
		})
	}
}

func TestModelFetcher_CodexEphemeralExpiredCredentials(t *testing.T) {
	// Refreshing rotates the refresh token; an ephemeral channel has nowhere to
	// persist it, so the fetch must refuse to refresh and fall back instead of
	// burning the user's refresh token.
	calls := 0
	client := httpclient.NewHttpClientWithClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})})
	key := `{"access_token":"expired-token","refresh_token":"some-refresh","expires_at":"2020-01-01T00:00:00Z"}`
	result, err := NewModelFetcher(client, nil).FetchModels(t.Context(), FetchModelsInput{
		ChannelType: channel.TypeCodex.String(), BaseURL: "https://chatgpt.com/backend-api/codex#", APIKey: &key,
	})
	require.NoError(t, err)
	require.Zero(t, calls)
	require.True(t, result.Fallback)
	require.Len(t, result.Models, len(codex.DefaultModels()))
}

func TestModelFetcher_CodexRawCLIAuthJSON(t *testing.T) {
	// The raw Codex CLI auth.json nests the tokens under "tokens"; the fetch
	// must decode that shape instead of silently falling back.
	calls := 0
	client := httpclient.NewHttpClientWithClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "Bearer cli-token", req.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"cli-model","visibility":"list"}]}`))}, nil
	})})
	key := `{"OPENAI_API_KEY":null,"tokens":{"access_token":"cli-token","refresh_token":"cli-refresh"},"last_refresh":"` +
		time.Now().Add(-time.Minute).Format(time.RFC3339Nano) + `"}`
	result, err := NewModelFetcher(client, nil).FetchModels(t.Context(), FetchModelsInput{
		ChannelType: channel.TypeCodex.String(), BaseURL: "https://chatgpt.com/backend-api/codex#", APIKey: &key,
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Nil(t, result.Error)
	require.False(t, result.Fallback)
	require.Equal(t, []ModelIdentify{{ID: "cli-model"}, {ID: "gpt-image-2"}}, result.Models)
}

func TestModelFetcher_CodexRelayModels(t *testing.T) {
	calls := 0
	client := httpclient.NewHttpClientWithClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "https://relay.example/v1/models", req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
		require.NotEmpty(t, req.URL.Query().Get("client_version"))
		require.Equal(t, "Bearer synthetic-api-key", req.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"relay-model","visibility":"list"}]}`))}, nil
	})})
	key := "synthetic-api-key"
	result, err := NewModelFetcher(client, nil).FetchModels(t.Context(), FetchModelsInput{
		ChannelType: channel.TypeCodex.String(), BaseURL: "https://relay.example/v1", APIKey: &key,
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, []ModelIdentify{{ID: "relay-model"}}, result.Models)
	require.False(t, result.Fallback)
}
