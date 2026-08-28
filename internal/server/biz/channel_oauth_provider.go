package biz

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/looplj/axonhub/internal/contexts"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer/openai/copilot"
)

// oauthCredentialEntry couples one named OAuth credential entry with its
// per-entry token getter and optional auto refresher.
type oauthCredentialEntry struct {
	ref       string
	name      string
	projectID string
	getter    oauth.TokenGetter
	refresher AutoRefresher
}

// EntryTokenProviderBuilder builds the token getter (and optional auto
// refresher) for a single named OAuth credential entry. The returned refresher
// may be nil when the provider manages no background refresh.
type EntryTokenProviderBuilder func(entry *objects.NamedOAuthCredentials) (getter oauth.TokenGetter, refresher AutoRefresher, err error)

// RotatingOAuthTokenProvider selects one of the channel's OAuth credential
// entries per request, following the same trace-sticky policy as the API key
// rotation. The selected entry ref is stored in the request context so that
// performance recording, auto-disable and scheduled recovery attribute failures
// to the exact credential that served the request.
type RotatingOAuthTokenProvider struct {
	channel *Channel
	entries []*oauthCredentialEntry
	byRef   map[string]*oauthCredentialEntry
	cache   *lru.Cache[string, string]
}

// newRotatingOAuthTokenProvider builds one token provider per OAuth credential
// entry. When the channel carries an API key override that matches an entry
// ref (channel key test flow), only that entry is built.
func newRotatingOAuthTokenProvider(
	ch *Channel,
	entries []objects.NamedOAuthCredentials,
	channelName string,
	build EntryTokenProviderBuilder,
) (*RotatingOAuthTokenProvider, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("channel %s has no oauth credentials", channelName)
	}

	// The single-entry test flow pins the channel to one credential by ref.
	if ch.apiKeyOverride != "" {
		for i := range entries {
			if entries[i].ID == ch.apiKeyOverride {
				entries = []objects.NamedOAuthCredentials{entries[i]}
				break
			}
		}
	}

	cache, _ := lru.New[string, string](traceStickyLRUSize)

	provider := &RotatingOAuthTokenProvider{
		channel: ch,
		byRef:   make(map[string]*oauthCredentialEntry, len(entries)),
		cache:   cache,
	}

	for i := range entries {
		entry := entries[i]
		if entry.Credentials == nil {
			return nil, fmt.Errorf("oauth credential %q of channel %s has no credentials", entry.ID, channelName)
		}

		getter, refresher, err := build(&entry)
		if err != nil {
			return nil, fmt.Errorf("failed to build token provider for oauth credential %q of channel %s: %w", entry.ID, channelName, err)
		}

		e := &oauthCredentialEntry{
			ref:       entry.ID,
			name:      entry.Name,
			projectID: entry.ProjectID,
			getter:    getter,
			refresher: refresher,
		}

		provider.entries = append(provider.entries, e)
		provider.byRef[e.ref] = e
	}

	return provider, nil
}

// projectIDFor returns the provider-side project bound to a credential entry.
func (p *RotatingOAuthTokenProvider) projectIDFor(ref string) string {
	if entry := p.byRef[ref]; entry != nil {
		return entry.projectID
	}

	return ""
}

// selectRef picks the credential ref for this request. Disabled entries are
// skipped; selection is trace-sticky via rendezvous hashing, matching the
// APIKeys rotation semantics.
func (p *RotatingOAuthTokenProvider) selectRef(ctx context.Context) string {
	enabled := p.enabledRefs()
	if len(enabled) == 0 {
		// Every entry is disabled yet the channel is still enabled; fall back to
		// all built entries rather than failing outright.
		for _, entry := range p.entries {
			enabled = append(enabled, entry.ref)
		}
	}

	if len(enabled) == 1 {
		return enabled[0]
	}

	if trace, ok := contexts.GetTrace(ctx); ok && trace != nil {
		if cached, ok := p.cache.Get(trace.TraceID); ok {
			return cached
		}

		selected := rendezvousSelect(enabled, trace.TraceID)
		p.cache.Add(trace.TraceID, selected)

		return selected
	}

	//nolint:gosec // not a security issue, just a random selection.
	return enabled[rand.IntN(len(enabled))]
}

func (p *RotatingOAuthTokenProvider) enabledRefs() []string {
	if p.channel == nil {
		return nil
	}

	all := p.channel.cachedEnabledCredentialRefs
	if all == nil {
		all = p.channel.Credentials.GetEnabledCredentialRefs(p.channel.DisabledAPIKeys)
	}

	enabled := make([]string, 0, len(all))
	for _, ref := range all {
		if _, ok := p.byRef[ref]; ok {
			enabled = append(enabled, ref)
		}
	}

	return enabled
}

// selectEntry picks the credential entry for this request and records its ref
// in the request context, so failure bookkeeping can attribute the request to
// the exact credential.
func (p *RotatingOAuthTokenProvider) selectEntry(ctx context.Context) (*oauthCredentialEntry, error) {
	ref := p.selectRef(ctx)

	entry := p.byRef[ref]
	if entry == nil {
		return nil, fmt.Errorf("oauth credential %q is not available on channel %s", ref, p.channel.Name)
	}

	contexts.WithChannelAPIKey(ctx, ref)

	if log.DebugEnabled(ctx) {
		log.Debug(ctx, "OAuth credential selected",
			log.String("channel", p.channel.Name),
			log.String("credential_ref", ref),
			log.String("credential_name", entry.name),
		)
	}

	return entry, nil
}

// Get implements oauth.TokenGetter.
func (p *RotatingOAuthTokenProvider) Get(ctx context.Context) (*oauth.OAuthCredentials, error) {
	entry, err := p.selectEntry(ctx)
	if err != nil {
		return nil, err
	}

	return entry.getter.Get(ctx)
}

// GetToken implements the copilot-style token provider interface so the
// rotating provider can be passed to transformers that expect GetToken.
func (p *RotatingOAuthTokenProvider) GetToken(ctx context.Context) (string, error) {
	entry, err := p.selectEntry(ctx)
	if err != nil {
		return "", err
	}

	creds, err := entry.getter.Get(ctx)
	if err != nil {
		return "", err
	}

	return creds.AccessToken, nil
}

// GetForRequest implements antigravity.RequestCredentialSource: it returns the
// selected credential together with the Google Cloud project bound to it.
func (p *RotatingOAuthTokenProvider) GetForRequest(ctx context.Context) (*oauth.OAuthCredentials, string, error) {
	entry, err := p.selectEntry(ctx)
	if err != nil {
		return nil, "", err
	}

	creds, err := entry.getter.Get(ctx)
	if err != nil {
		return nil, "", err
	}

	return creds, p.projectIDFor(entry.ref), nil
}

// StartAutoRefresh starts the background refresh of every per-entry provider.
func (p *RotatingOAuthTokenProvider) StartAutoRefresh(ctx context.Context, opts oauth.AutoRefreshOptions) {
	for _, entry := range p.entries {
		if entry.refresher != nil {
			entry.refresher.StartAutoRefresh(ctx, opts)
		}
	}
}

// StopAutoRefresh stops the background refresh of every per-entry provider.
func (p *RotatingOAuthTokenProvider) StopAutoRefresh() {
	for _, entry := range p.entries {
		if entry.refresher != nil {
			entry.refresher.StopAutoRefresh()
		}
	}
}

// assembleOAuthTokenProvider builds the token provider for an OAuth channel:
// a rotating provider that selects one named credential entry per request.
// It also serves single-entry channels (including the legacy single OAuth
// layout), whose rotating wrapper always writes the selected credential ref
// into the request context for per-credential disable bookkeeping.
func (svc *ChannelService) assembleOAuthTokenProvider(
	c *ent.Channel,
	ch *Channel,
	buildEntry func(entry *objects.NamedOAuthCredentials, onRefreshed func(ctx context.Context, refreshed *oauth.OAuthCredentials) error) (oauth.TokenGetter, AutoRefresher, error),
) (*RotatingOAuthTokenProvider, error) {
	return newRotatingOAuthTokenProvider(ch, c.Credentials.GetAllOAuthCredentials(), c.Name,
		func(entry *objects.NamedOAuthCredentials) (oauth.TokenGetter, AutoRefresher, error) {
			return buildEntry(entry, svc.onOAuthEntryRefreshed(c, entry.ID))
		})
}

// antigravityCredentialEntries normalizes an antigravity channel's credentials
// into named entries. Entry-based channels are taken as-is; legacy channels
// keep the "refreshToken|projectID" composite in APIKey and are parsed into a
// single sentinel entry.
func antigravityCredentialEntries(c *ent.Channel) []objects.NamedOAuthCredentials {
	if len(c.Credentials.OAuths) > 0 {
		return c.Credentials.OAuths
	}

	refreshToken, projectID := parseAntigravityCreds(c.Credentials.APIKey)
	if refreshToken == "" {
		return nil
	}

	return []objects.NamedOAuthCredentials{{
		ID:        objects.OAuthCredentialRef,
		ProjectID: projectID,
		Credentials: &oauth.OAuthCredentials{
			RefreshToken: refreshToken,
		},
	}}
}

// parseAntigravityCreds splits the legacy "refreshToken|projectID" composite.
func parseAntigravityCreds(apiKey string) (string, string) {
	parts := strings.Split(apiKey, "|")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}

	return apiKey, ""
}

// copilotTokenGetterAdapter adapts the Copilot token exchanger to the generic
// oauth.TokenGetter interface consumed by the rotating provider.
type copilotTokenGetterAdapter struct {
	provider *copilot.CopilotTokenProvider
}

func (a copilotTokenGetterAdapter) Get(ctx context.Context) (*oauth.OAuthCredentials, error) {
	token, err := a.provider.GetToken(ctx)
	if err != nil {
		return nil, err
	}

	return &oauth.OAuthCredentials{AccessToken: token}, nil
}
