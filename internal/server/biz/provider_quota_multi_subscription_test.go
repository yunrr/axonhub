package biz

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/server/biz/provider_quota"
	"github.com/stretchr/testify/require"
)

type recordingOAuthQuotaChecker struct {
	mu           sync.Mutex
	accessTokens []string
}

func (c *recordingOAuthQuotaChecker) CheckQuota(_ context.Context, ch *ent.Channel) (provider_quota.QuotaData, error) {
	accessToken := ch.Credentials.OAuth.AccessToken

	c.mu.Lock()
	c.accessTokens = append(c.accessTokens, accessToken)
	c.mu.Unlock()

	status := "available"
	if accessToken == "exhausted-token" {
		status = "exhausted"
	}

	return provider_quota.QuotaData{
		Status:       status,
		ProviderType: "codex",
		Ready:        status == "available",
		RawData:      map[string]any{"access_token": accessToken},
	}, nil
}

func (c *recordingOAuthQuotaChecker) SupportsChannel(*ent.Channel) bool {
	return true
}

func TestCheckQuotaData_ChecksEachNamedOAuthSubscription(t *testing.T) {
	svc := &ProviderQuotaService{}
	checker := &recordingOAuthQuotaChecker{}
	channel := &ent.Channel{
		ID: 42,
		Credentials: objects.ChannelCredentials{
			OAuths: []objects.NamedOAuthCredentials{
				{ID: "work", Name: "Work", Credentials: &objects.OAuthCredentials{AccessToken: "work-token"}},
				{ID: "personal", Name: "Personal", Credentials: &objects.OAuthCredentials{AccessToken: "exhausted-token"}},
			},
		},
	}

	result, err := svc.checkQuotaData(context.Background(), channel, "codex", checker, time.Now())

	require.NoError(t, err)
	// 订阅是并发检查的，只断言全部被检查到，不断言顺序。
	sortedTokens := slices.Clone(checker.accessTokens)
	slices.Sort(sortedTokens)
	require.Equal(t, []string{"exhausted-token", "work-token"}, sortedTokens)
	require.Equal(t, "available", result.Status)
	require.Len(t, result.RawData["_subscriptions"], 2)
}

func TestCheckQuotaData_LegacyOAuthDoesNotCreateSubscriptionSnapshot(t *testing.T) {
	svc := &ProviderQuotaService{}
	checker := &recordingOAuthQuotaChecker{}
	channel := &ent.Channel{
		ID: 42,
		Credentials: objects.ChannelCredentials{
			OAuth: &objects.OAuthCredentials{AccessToken: "legacy-token"},
		},
	}

	result, err := svc.checkQuotaData(context.Background(), channel, "codex", checker, time.Now())

	require.NoError(t, err)
	require.Equal(t, []string{"legacy-token"}, checker.accessTokens)
	_, hasSubscriptions := result.RawData["_subscriptions"]
	require.False(t, hasSubscriptions)
}

func TestAggregateOAuthQuotaData(t *testing.T) {
	svc := &ProviderQuotaService{}
	checks := []quotaSubscriptionCheck{
		{
			Entry: objects.NamedOAuthCredentials{ID: "sub-available", Name: "work"},
			Data: provider_quota.QuotaData{
				Status:       "available",
				ProviderType: "codex",
				Ready:        true,
				RawData:      map[string]any{"plan_type": "plus"},
				Limits: []provider_quota.QuotaLimitStatus{{
					Type:       provider_quota.QuotaLimitTypeToken,
					Status:     "available",
					UsageRatio: 0.25,
					Ready:      true,
				}},
			},
		},
		{
			Entry: objects.NamedOAuthCredentials{ID: "sub-exhausted", Name: "personal"},
			Data: provider_quota.QuotaData{
				Status:       "exhausted",
				ProviderType: "codex",
				Ready:        false,
				RawData:      map[string]any{"plan_type": "free"},
				Limits: []provider_quota.QuotaLimitStatus{{
					Type:       provider_quota.QuotaLimitTypeToken,
					Status:     "exhausted",
					UsageRatio: 1,
				}},
			},
		},
	}

	result := svc.aggregateOAuthQuotaData("codex", checks)

	require.Equal(t, "available", result.Status, "one available subscription keeps the channel available")
	require.True(t, result.Ready)
	require.Len(t, result.RawData["_subscriptions"], 2)
	require.Equal(t, 1, result.RawData["available_subscription_count"])
	require.Equal(t, "exhausted", result.RawData["_subscriptions"].([]map[string]any)[1]["status"])
	require.Equal(t, "plus", result.RawData["plan_type"], "the representative subscription matches the channel-level status so the collapsed row is consistent")
	require.Empty(t, result.Limits, "one subscription's limits must not override the aggregated channel status in routing")
	require.Empty(t, extractQuotaCacheLimits(result.RawData), "representative UI limits must not be restored into the routing cache")
}

func TestCheckQuotaData_AllSubscriptionsFailedReturnsError(t *testing.T) {
	svc := &ProviderQuotaService{}
	checker := &failingOAuthQuotaChecker{}
	channel := &ent.Channel{
		ID: 42,
		Credentials: objects.ChannelCredentials{
			OAuths: []objects.NamedOAuthCredentials{
				{ID: "sub-1", Name: "one", Credentials: &objects.OAuthCredentials{AccessToken: "t1"}},
				{ID: "sub-2", Name: "two", Credentials: &objects.OAuthCredentials{AccessToken: "t2"}},
			},
		},
	}

	_, err := svc.checkQuotaData(context.Background(), channel, "codex", checker, time.Now())
	require.Error(t, err, "all subscriptions failing must surface the error so the caller records it in the error backoff ledger")
	require.Contains(t, err.Error(), "all 2 subscription quota checks failed")
}

type failingOAuthQuotaChecker struct{}

func (c *failingOAuthQuotaChecker) CheckQuota(context.Context, *ent.Channel) (provider_quota.QuotaData, error) {
	return provider_quota.QuotaData{}, errors.New("quota request failed")
}

func (c *failingOAuthQuotaChecker) SupportsChannel(*ent.Channel) bool {
	return true
}

func TestAggregateOAuthQuotaData_AllChecksFailed(t *testing.T) {
	svc := &ProviderQuotaService{}
	result := svc.aggregateOAuthQuotaData("codex", []quotaSubscriptionCheck{
		{Entry: objects.NamedOAuthCredentials{ID: "sub-1"}, Err: errors.New("expired")},
		{Entry: objects.NamedOAuthCredentials{ID: "sub-2"}, Err: errors.New("unauthorized")},
	})

	require.Equal(t, "unknown", result.Status)
	require.False(t, result.Ready)
	require.Equal(t, "all OAuth subscriptions quota checks failed", result.RawData["error"])
	require.Len(t, result.RawData["_subscriptions"], 2)
	require.Equal(t, "sub-1", result.RawData["_subscriptions"].([]map[string]any)[0]["id"])
}
