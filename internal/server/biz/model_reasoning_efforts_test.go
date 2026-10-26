package biz

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/enttest"
	"github.com/looplj/axonhub/internal/ent/model"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/pkg/xcache"
)

func setupTestModelService(t *testing.T, cacheConfig xcache.Config) (*ModelService, *ent.Client, context.Context) {
	t.Helper()

	client := enttest.NewEntClient(t, "sqlite3", "file:ent?mode=memory&_fk=0")
	t.Cleanup(func() { client.Close() })

	ctx := authz.WithTestBypass(ent.NewContext(context.Background(), client))

	modelService := NewModelService(ModelServiceParams{
		Ent:         client,
		CacheConfig: cacheConfig,
	})

	return modelService, client, ctx
}

func createReasoningEffortModel(t *testing.T, client *ent.Client, ctx context.Context, modelID string, efforts []string, status model.Status) *ent.Model {
	t.Helper()

	return client.Model.Create().
		SetModelID(modelID).
		SetName(modelID).
		SetDeveloper("test").
		SetIcon("test").
		SetGroup("test").
		SetSettings(&objects.ModelSettings{}).
		SetModelCard(&objects.ModelCard{ReasoningEfforts: efforts}).
		SetStatus(status).
		SaveX(ctx)
}

func TestModelService_GetModelReasoningEfforts(t *testing.T) {
	modelService, client, ctx := setupTestModelService(t, xcache.Config{Mode: xcache.ModeMemory})

	createReasoningEffortModel(t, client, ctx, "gpt-5.4", []string{"low", "high"}, model.StatusEnabled)
	createReasoningEffortModel(t, client, ctx, "gpt-5.4-unknown", nil, model.StatusEnabled)
	createReasoningEffortModel(t, client, ctx, "gpt-5.4-disabled", []string{"low"}, model.StatusDisabled)

	efforts, err := modelService.GetModelReasoningEfforts(ctx, "gpt-5.4", "gpt-5.4-unknown", "gpt-5.4-disabled", "gpt-5.4-missing", "")
	require.NoError(t, err)

	assert.Equal(t, []string{"low", "high"}, efforts["gpt-5.4"])

	unknown, ok := efforts["gpt-5.4-unknown"]
	assert.True(t, ok, "a model without declared levels must be present with a nil value")
	assert.Nil(t, unknown)

	assert.NotContains(t, efforts, "gpt-5.4-disabled")
	assert.NotContains(t, efforts, "gpt-5.4-missing")
	assert.NotContains(t, efforts, "")
}

// TestModelService_GetModelReasoningEfforts_Caches verifies a cache hit skips the database
// and that a write through ModelService invalidates the entry.
func TestModelService_GetModelReasoningEfforts_Caches(t *testing.T) {
	modelService, client, ctx := setupTestModelService(t, xcache.Config{Mode: xcache.ModeMemory})

	row := createReasoningEffortModel(t, client, ctx, "gpt-5.4", []string{"low"}, model.StatusEnabled)

	efforts, err := modelService.GetModelReasoningEfforts(ctx, "gpt-5.4")
	require.NoError(t, err)
	require.Equal(t, []string{"low"}, efforts["gpt-5.4"])

	// Change the row behind the service: the cached value must still be served.
	client.Model.UpdateOneID(row.ID).
		SetModelCard(&objects.ModelCard{ReasoningEfforts: []string{"high"}}).
		SaveX(ctx)

	efforts, err = modelService.GetModelReasoningEfforts(ctx, "gpt-5.4")
	require.NoError(t, err)
	assert.Equal(t, []string{"low"}, efforts["gpt-5.4"], "expected the cached levels to be served")

	// A write through the service invalidates the entry.
	_, err = modelService.UpdateModel(ctx, row.ID, &ent.UpdateModelInput{
		ModelCard: &objects.ModelCard{ReasoningEfforts: []string{"high"}},
	})
	require.NoError(t, err)

	efforts, err = modelService.GetModelReasoningEfforts(ctx, "gpt-5.4")
	require.NoError(t, err)
	assert.Equal(t, []string{"high"}, efforts["gpt-5.4"])
}

func TestModelService_GetModelReasoningEfforts_Invalidation(t *testing.T) {
	modelService, client, ctx := setupTestModelService(t, xcache.Config{Mode: xcache.ModeMemory})

	// Negative caching: a missing model stays missing until the id is created through the service.
	efforts, err := modelService.GetModelReasoningEfforts(ctx, "gpt-5.5")
	require.NoError(t, err)
	assert.NotContains(t, efforts, "gpt-5.5")

	createReasoningEffortModel(t, client, ctx, "gpt-5.5", []string{"low"}, model.StatusEnabled)

	efforts, err = modelService.GetModelReasoningEfforts(ctx, "gpt-5.5")
	require.NoError(t, err)
	assert.NotContains(t, efforts, "gpt-5.5", "expected the negative cache entry to be served")

	_, err = modelService.CreateModel(ctx, ent.CreateModelInput{
		ModelID:   "gpt-5.6",
		Name:      "gpt-5.6",
		Developer: "test",
		ModelCard: &objects.ModelCard{ReasoningEfforts: []string{"low"}},
		Settings:  &objects.ModelSettings{},
	})
	require.NoError(t, err)

	efforts, err = modelService.GetModelReasoningEfforts(ctx, "gpt-5.6")
	require.NoError(t, err)
	assert.NotContains(t, efforts, "gpt-5.6", "a disabled model must not resolve")

	created := client.Model.Query().Where(model.ModelID("gpt-5.6")).OnlyX(ctx)

	_, err = modelService.UpdateModelStatus(ctx, created.ID, model.StatusEnabled)
	require.NoError(t, err)

	efforts, err = modelService.GetModelReasoningEfforts(ctx, "gpt-5.6")
	require.NoError(t, err)
	assert.Equal(t, []string{"low"}, efforts["gpt-5.6"])

	// UpdateModel clears the entry, including after a model id rename.
	row := createReasoningEffortModel(t, client, ctx, "gpt-5.7", []string{"low"}, model.StatusEnabled)

	_, err = modelService.GetModelReasoningEfforts(ctx, "gpt-5.7")
	require.NoError(t, err)

	renamed := "gpt-5.7-renamed"

	_, err = modelService.UpdateModel(ctx, row.ID, &ent.UpdateModelInput{ModelID: &renamed})
	require.NoError(t, err)

	efforts, err = modelService.GetModelReasoningEfforts(ctx, "gpt-5.7")
	require.NoError(t, err)
	assert.NotContains(t, efforts, "gpt-5.7", "the previous model id must be invalidated")

	// Bulk status changes and deletes invalidate as well.
	require.NoError(t, modelService.BulkDisableModels(ctx, []int{row.ID}))

	efforts, err = modelService.GetModelReasoningEfforts(ctx, renamed)
	require.NoError(t, err)
	assert.NotContains(t, efforts, renamed, "a disabled model must not resolve")

	require.NoError(t, modelService.BulkEnableModels(ctx, []int{row.ID}))

	efforts, err = modelService.GetModelReasoningEfforts(ctx, renamed)
	require.NoError(t, err)
	assert.Equal(t, []string{"low"}, efforts[renamed])

	require.NoError(t, modelService.BulkDeleteModels(ctx, []int{row.ID}))

	efforts, err = modelService.GetModelReasoningEfforts(ctx, renamed)
	require.NoError(t, err)
	assert.NotContains(t, efforts, renamed, "a deleted model must not resolve")
}

// TestModelService_GetModelReasoningEfforts_NoCache verifies the noop cache degrades to
// direct database reads without changing results.
func TestModelService_GetModelReasoningEfforts_NoCache(t *testing.T) {
	modelService, client, ctx := setupTestModelService(t, xcache.Config{})

	createReasoningEffortModel(t, client, ctx, "gpt-5.4", []string{"low"}, model.StatusEnabled)

	efforts, err := modelService.GetModelReasoningEfforts(ctx, "gpt-5.4")
	require.NoError(t, err)
	assert.Equal(t, []string{"low"}, efforts["gpt-5.4"])
}
