package provider_quota

import (
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/llm/oauth"
)

// resolveChannelOAuthCredentials returns the OAuth credentials quota checks
// run against. Multi-entry channels (named subscriptions) are checked on
// their first entry; legacy single-credential channels fall back to the
// stored OAuth field / APIKey JSON.
func resolveChannelOAuthCredentials(ch *ent.Channel) (*oauth.OAuthCredentials, error) {
	if entries := ch.Credentials.GetAllOAuthCredentials(); len(entries) > 0 && entries[0].Credentials != nil {
		return entries[0].Credentials, nil
	}

	return ch.Credentials.ResolveOAuthCredentials()
}
