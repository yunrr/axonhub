package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/enttest"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/pkg/xcache"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
)

func TestSplitAutoReasoningEffortModel(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		wantBaseModel  string
		wantEffort     string
		wantNormalized bool
	}{
		{
			name:           "supports xhigh",
			model:          "gpt-5.4-xhigh",
			wantBaseModel:  "gpt-5.4",
			wantEffort:     "xhigh",
			wantNormalized: true,
		},
		{
			name:           "supports max",
			model:          "gpt-5.4-max",
			wantBaseModel:  "gpt-5.4",
			wantEffort:     "max",
			wantNormalized: true,
		},
		{
			name:           "supports none",
			model:          "gpt-5.4-none",
			wantBaseModel:  "gpt-5.4",
			wantEffort:     "none",
			wantNormalized: true,
		},
		{
			name:           "supports minimal",
			model:          "gpt-5.4-minimal",
			wantBaseModel:  "gpt-5.4",
			wantEffort:     "minimal",
			wantNormalized: true,
		},
		{
			name:           "keeps qwen max model unchanged",
			model:          "qwen3.7-max",
			wantNormalized: false,
		},
		{
			name:           "keeps unknown suffix unchanged",
			model:          "gpt-5.4-ultra",
			wantNormalized: false,
		},
		{
			name:           "keeps model without suffix unchanged",
			model:          "gpt-5.4",
			wantNormalized: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseModel, effort, ok := splitAutoReasoningEffortModel(tt.model)
			assert.Equal(t, tt.wantNormalized, ok)
			assert.Equal(t, tt.wantBaseModel, baseModel)
			assert.Equal(t, tt.wantEffort, effort)
		})
	}
}

func TestAutoReasoningEffortMiddleware_OnInboundLlmRequest(t *testing.T) {
	type testModel struct {
		modelID string
		efforts []string
	}

	tests := []struct {
		name           string
		settings       biz.SystemModelSettings
		models         []testModel
		withoutService bool
		request        *llm.Request
		wantModel      string
		wantEffort     string
		wantErr        string
	}{
		{
			name: "disabled setting leaves request unchanged",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: false,
			},
			models:  []testModel{{modelID: "gpt-5.4", efforts: []string{"low", "high", "xhigh"}}},
			request: &llm.Request{Model: "gpt-5.4-xhigh"},

			wantModel:  "gpt-5.4-xhigh",
			wantEffort: "",
		},
		{
			name: "applies level declared by the model card",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models:  []testModel{{modelID: "gpt-5.4", efforts: []string{"low", "high", "xhigh"}}},
			request: &llm.Request{Model: "gpt-5.4-xhigh"},

			wantModel:  "gpt-5.4",
			wantEffort: "xhigh",
		},
		{
			name: "applies none declared by the model card",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models:  []testModel{{modelID: "gpt-5.4", efforts: []string{"none", "low", "high"}}},
			request: &llm.Request{Model: "gpt-5.4-none"},

			wantModel:  "gpt-5.4",
			wantEffort: "none",
		},
		{
			name: "matches declared level case-insensitively",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models:  []testModel{{modelID: "gpt-5.4", efforts: []string{"Low", "High"}}},
			request: &llm.Request{Model: "gpt-5.4-HIGH"},

			wantModel:  "gpt-5.4",
			wantEffort: "high",
		},
		{
			name: "rejects level the model card does not declare",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models:  []testModel{{modelID: "gpt-5.4", efforts: []string{"low", "medium", "high"}}},
			request: &llm.Request{Model: "gpt-5.4-minimal"},

			wantErr: `model "gpt-5.4" does not support reasoning effort "minimal" (supported: low, medium, high)`,
		},
		{
			name: "accepts every unified level when the model card declares none",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models:  []testModel{{modelID: "gpt-5.4"}},
			request: &llm.Request{Model: "gpt-5.4-none"},

			wantModel:  "gpt-5.4",
			wantEffort: "none",
		},
		{
			name: "accepts unified levels when the base model is not configured",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			request: &llm.Request{Model: "gpt-5.4-minimal"},

			wantModel:  "gpt-5.4",
			wantEffort: "minimal",
		},
		{
			name: "keeps an exact model library entry unchanged",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models: []testModel{
				{modelID: "gpt-5.4", efforts: []string{"low", "high"}},
				{modelID: "gpt-5.4-high", efforts: []string{"low", "high"}},
			},
			request: &llm.Request{Model: "gpt-5.4-high"},

			wantModel:  "gpt-5.4-high",
			wantEffort: "",
		},
		{
			name: "keeps qwen max model unchanged",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models:  []testModel{{modelID: "qwen3.7", efforts: []string{"max"}}},
			request: &llm.Request{Model: "qwen3.7-max"},

			wantModel:  "qwen3.7-max",
			wantEffort: "",
		},
		{
			name: "suffix reasoning effort overrides explicit request value",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models: []testModel{{modelID: "gpt-5.4", efforts: []string{"low", "high", "xhigh"}}},
			request: &llm.Request{
				Model:           "gpt-5.4-xhigh",
				ReasoningEffort: "high",
			},

			wantModel:  "gpt-5.4",
			wantEffort: "xhigh",
		},
		{
			name: "unsupported suffix is ignored",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			models:  []testModel{{modelID: "gpt-5.4", efforts: []string{"low", "high"}}},
			request: &llm.Request{Model: "gpt-5.4-ultra"},

			wantModel:  "gpt-5.4-ultra",
			wantEffort: "",
		},
		{
			name: "works without a model service",
			settings: biz.SystemModelSettings{
				AutoReasoningEffort: true,
			},
			withoutService: true,
			request:        &llm.Request{Model: "gpt-5.4-none"},

			wantModel:  "gpt-5.4",
			wantEffort: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := authz.WithTestBypass(context.Background())

			client := enttest.NewEntClient(t, "sqlite3", "file:ent?mode=memory&_fk=0")
			defer client.Close()

			ctx = ent.NewContext(ctx, client)
			_, _, systemService, _ := setupTestServices(t, client)

			err := systemService.SetModelSettings(ctx, tt.settings)
			require.NoError(t, err)

			for _, m := range tt.models {
				client.Model.Create().
					SetModelID(m.modelID).
					SetName(m.modelID).
					SetDeveloper("test").
					SetIcon("test").
					SetGroup("test").
					SetSettings(&objects.ModelSettings{}).
					SetModelCard(&objects.ModelCard{ReasoningEfforts: m.efforts}).
					SetStatus("enabled").
					SaveX(ctx)
			}

			var modelService *biz.ModelService
			if !tt.withoutService {
				modelService = biz.NewModelService(biz.ModelServiceParams{
					Ent:           client,
					CacheConfig:   xcache.Config{Mode: xcache.ModeMemory},
					SystemService: systemService,
				})
			}

			middleware := applyAutoReasoningEffort(systemService, modelService)
			result, err := middleware.OnInboundLlmRequest(ctx, tt.request)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantModel, result.Model)
			assert.Equal(t, tt.wantEffort, result.ReasoningEffort)
		})
	}
}

// TestAutoReasoningEffortMiddleware_CachesModelCard verifies the model card lookup is cached:
// a direct database change is invisible until the cache is invalidated through ModelService.
func TestAutoReasoningEffortMiddleware_CachesModelCard(t *testing.T) {
	ctx := authz.WithTestBypass(context.Background())

	client := enttest.NewEntClient(t, "sqlite3", "file:ent?mode=memory&_fk=0")
	defer client.Close()

	ctx = ent.NewContext(ctx, client)
	_, _, systemService, _ := setupTestServices(t, client)

	require.NoError(t, systemService.SetModelSettings(ctx, biz.SystemModelSettings{AutoReasoningEffort: true}))

	modelRow := client.Model.Create().
		SetModelID("gpt-5.4").
		SetName("GPT-5.4").
		SetDeveloper("test").
		SetIcon("test").
		SetGroup("test").
		SetSettings(&objects.ModelSettings{}).
		SetModelCard(&objects.ModelCard{ReasoningEfforts: []string{"low", "high"}}).
		SetStatus("enabled").
		SaveX(ctx)

	modelService := biz.NewModelService(biz.ModelServiceParams{
		Ent:           client,
		CacheConfig:   xcache.Config{Mode: xcache.ModeMemory},
		SystemService: systemService,
	})

	middleware := applyAutoReasoningEffort(systemService, modelService)

	// Warm the cache: "minimal" is rejected by the declared levels.
	_, err := middleware.OnInboundLlmRequest(ctx, &llm.Request{Model: "gpt-5.4-minimal"})
	require.Error(t, err)

	// Change the card behind the service: the cached levels must still apply.
	client.Model.UpdateOneID(modelRow.ID).
		SetModelCard(&objects.ModelCard{ReasoningEfforts: []string{"minimal"}}).
		SaveX(ctx)

	_, err = middleware.OnInboundLlmRequest(ctx, &llm.Request{Model: "gpt-5.4-minimal"})
	require.Error(t, err, "cached levels should still reject minimal")

	// Invalidate through the service: the new levels apply immediately.
	_, err = modelService.UpdateModel(ctx, modelRow.ID, &ent.UpdateModelInput{
		ModelCard: &objects.ModelCard{ReasoningEfforts: []string{"minimal"}},
	})
	require.NoError(t, err)

	normalized, err := middleware.OnInboundLlmRequest(ctx, &llm.Request{Model: "gpt-5.4-minimal"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.4", normalized.Model)
	assert.Equal(t, "minimal", normalized.ReasoningEffort)
}
