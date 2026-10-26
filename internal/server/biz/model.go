package biz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/samber/lo"
	"go.uber.org/fx"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/contexts"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/channel"
	"github.com/looplj/axonhub/internal/ent/model"
	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/pkg/xcache"
	"github.com/looplj/axonhub/internal/pkg/xerrors"
	"github.com/looplj/axonhub/internal/pkg/xregexp"
	"github.com/looplj/axonhub/internal/pkg/xtime"
	"github.com/looplj/axonhub/internal/scopes"
	"github.com/looplj/axonhub/llm/httpclient"
)

type ModelServiceParams struct {
	fx.In

	CacheConfig    xcache.Config
	ChannelService *ChannelService
	SystemService  *SystemService
	Ent            *ent.Client
}

func NewModelService(params ModelServiceParams) *ModelService {
	return &ModelService{
		AbstractService: &AbstractService{
			db: params.Ent,
		},
		channelService:        params.ChannelService,
		systemService:         params.SystemService,
		reasoningEffortsCache: xcache.NewFromConfig[xcache.Entry[[]string]](params.CacheConfig),
	}
}

type ModelService struct {
	*AbstractService

	channelService *ChannelService
	systemService  *SystemService
	// reasoningEffortsCache caches the reasoning effort levels declared by each model
	// card. Keys are model ids; negative entries mark models missing from the library.
	reasoningEffortsCache xcache.Cache[xcache.Entry[[]string]]
}

// validateModelSettings validates regex patterns in model settings.
func (svc *ModelService) validateModelSettings(settings *objects.ModelSettings) error {
	return validateModelSettings(settings)
}

func validateModelSettings(settings *objects.ModelSettings) error {
	if settings == nil {
		return nil
	}

	settings.NormalizeRoutingPolicy()
	if !objects.IsValidLoadBalancerStrategy(settings.LoadBalancerStrategy) {
		return fmt.Errorf("invalid load balancer strategy %q", settings.LoadBalancerStrategy)
	}
	if !objects.IsValidTraceStickyMode(settings.TraceStickyMode) {
		return fmt.Errorf("invalid trace sticky mode %q", settings.TraceStickyMode)
	}

	if len(settings.Associations) == 0 {
		return nil
	}

	for _, assoc := range settings.Associations {
		if assoc == nil {
			continue
		}

		if err := validateModelAssociationWhen(assoc.When); err != nil {
			return fmt.Errorf("invalid when condition: %w", err)
		}

		// Validate ChannelRegex pattern
		if assoc.ChannelRegex != nil && assoc.ChannelRegex.Pattern != "" {
			if err := xregexp.ValidateRegex(assoc.ChannelRegex.Pattern); err != nil {
				return fmt.Errorf("invalid regex pattern in channel_regex association: %w", err)
			}
		}

		// Validate ChannelTagsRegex pattern
		if assoc.ChannelTagsRegex != nil && assoc.ChannelTagsRegex.Pattern != "" {
			if err := xregexp.ValidateRegex(assoc.ChannelTagsRegex.Pattern); err != nil {
				return fmt.Errorf("invalid regex pattern in channel_tags_regex association: %w", err)
			}
		}

		// Validate Regex pattern
		if assoc.Regex != nil && assoc.Regex.Pattern != "" {
			if err := xregexp.ValidateRegex(assoc.Regex.Pattern); err != nil {
				return fmt.Errorf("invalid regex pattern in regex association: %w", err)
			}
		}

		// Validate Exclude patterns
		if assoc.Regex != nil && len(assoc.Regex.Exclude) > 0 {
			for _, exclude := range assoc.Regex.Exclude {
				if exclude.ChannelNamePattern != "" {
					if err := xregexp.ValidateRegex(exclude.ChannelNamePattern); err != nil {
						return fmt.Errorf("invalid regex pattern in exclude rule: %w", err)
					}
				}
			}
		}

		if assoc.ModelID != nil && len(assoc.ModelID.Exclude) > 0 {
			for _, exclude := range assoc.ModelID.Exclude {
				if exclude.ChannelNamePattern != "" {
					if err := xregexp.ValidateRegex(exclude.ChannelNamePattern); err != nil {
						return fmt.Errorf("invalid regex pattern in exclude rule: %w", err)
					}
				}
			}
		}
	}

	return nil
}

func validateModelAssociationWhen(when *objects.ModelAssociationWhen) error {
	if when == nil {
		return nil
	}

	if !when.Enabled {
		return nil
	}

	if when.Condition == nil {
		return fmt.Errorf("at least one supported when condition is required")
	}

	return validateFilterConditionNode(when.Condition, filterValidationOptions{
		AllowNestedGroups: true,
		MaxNestedLevels:   3,
	})
}

type filterValidationOptions struct {
	AllowNestedGroups bool
	MaxNestedLevels   int
}

func validateFilterConditionNode(condition *objects.Condition, opts filterValidationOptions) error {
	return validateFilterConditionNodeAtDepth(condition, opts, 1, true)
}

func validateFilterConditionNodeAtDepth(condition *objects.Condition, opts filterValidationOptions, depth int, requireGroup bool) error {
	if condition == nil {
		return nil
	}

	nodeType := condition.Type
	if nodeType == "" {
		nodeType = objects.ConditionTypeGroup
	}

	if requireGroup && nodeType != objects.ConditionTypeGroup {
		return fmt.Errorf("root when condition must be a group")
	}

	switch nodeType {
	case objects.ConditionTypeGroup:
		if len(condition.Conditions) == 0 {
			return fmt.Errorf("condition requires at least one condition or group")
		}

		if opts.MaxNestedLevels > 0 && depth > opts.MaxNestedLevels {
			return fmt.Errorf("condition nesting depth must not exceed %d", opts.MaxNestedLevels)
		}

		for _, child := range condition.Conditions {
			if child.Type == objects.ConditionTypeGroup {
				if !opts.AllowNestedGroups {
					return fmt.Errorf("nested condition groups are not allowed")
				}
			}

			if err := validateFilterConditionNodeAtDepth(&child, opts, depth+1, false); err != nil {
				return err
			}
		}

		return nil
	case "", objects.ConditionTypeCondition:
		return validateFilterLeaf(*condition)
	default:
		return fmt.Errorf("unsupported condition type %q", condition.Type)
	}
}

func validateFilterLeaf(condition objects.Condition) error {
	if condition.Field == "" {
		return fmt.Errorf("condition field is required")
	}

	if strings.HasPrefix(condition.Field, objects.ModelAssociationConditionFieldRequestHeaderPrefix) {
		return validateRequestHeaderLeaf(condition)
	}

	switch condition.Field {
	case objects.ModelAssociationConditionFieldPromptTokens:
		return validatePromptTokensLeaf(condition)
	case objects.ModelAssociationConditionFieldStream:
		return validateBoolEqualityLeaf(condition, objects.ModelAssociationConditionFieldStream)
	case objects.ModelAssociationConditionFieldRequestFormat,
		objects.ModelAssociationConditionFieldReasoningEffort:
		return validateStringEqualityLeaf(condition, condition.Field)
	case objects.ModelAssociationConditionFieldDailyTime:
		return validateDailyTimeLeaf(condition)
	case objects.ModelAssociationConditionFieldHasImage,
		objects.ModelAssociationConditionFieldHasVideo,
		objects.ModelAssociationConditionFieldHasDocument,
		objects.ModelAssociationConditionFieldHasAudio:
		return validateBoolEqualityLeaf(condition, condition.Field)
	default:
		return fmt.Errorf("unsupported condition field %q", condition.Field)
	}
}

func validatePromptTokensLeaf(condition objects.Condition) error {
	switch condition.Operator {
	case "lt", "lte", "gt", "gte", "<", "<=", ">", ">=":
	default:
		return fmt.Errorf("unsupported condition operator %q for prompt_tokens", condition.Operator)
	}

	value, ok, err := filterConditionValueToInt64(condition)
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("condition value for prompt_tokens must be an integer")
	}

	if value < 0 {
		return fmt.Errorf("prompt_tokens must be greater than or equal to 0")
	}

	return nil
}

func validateBoolEqualityLeaf(condition objects.Condition, field string) error {
	switch condition.Operator {
	case "eq", "ne", "=", "==", "!=":
	default:
		return fmt.Errorf("unsupported condition operator %q for %s", condition.Operator, field)
	}

	switch condition.Value.(type) {
	case bool:
		return nil
	default:
		return fmt.Errorf("condition value for %s must be a boolean, got %T", field, condition.Value)
	}
}

func validateStringEqualityLeaf(condition objects.Condition, field string) error {
	switch condition.Operator {
	case "eq", "ne", "=", "==", "!=":
	default:
		return fmt.Errorf("unsupported condition operator %q for %s", condition.Operator, field)
	}

	value, ok := condition.Value.(string)
	if !ok || value == "" {
		return fmt.Errorf("condition value for %s must be a non-empty string", field)
	}

	return nil
}

func validateDailyTimeLeaf(condition objects.Condition) error {
	switch condition.Operator {
	case "within", "not_within":
	default:
		return fmt.Errorf("unsupported condition operator %q for daily_time", condition.Operator)
	}

	value, ok := condition.Value.(string)
	if !ok || value == "" {
		return fmt.Errorf("condition value for daily_time must be a daily time range")
	}

	if _, _, err := xtime.ParseDailyTimeRange(value); err != nil {
		return err
	}

	return nil
}

func validateRequestHeaderLeaf(condition objects.Condition) error {
	switch strings.TrimSpace(strings.ToLower(condition.Operator)) {
	case "eq", "ne", "=", "==", "!=", "<>", "contains", "not_contains", "start_with", "end_with":
	default:
		return fmt.Errorf("unsupported condition operator %q for request_header", condition.Operator)
	}

	headerName := strings.TrimSpace(strings.TrimPrefix(condition.Field, objects.ModelAssociationConditionFieldRequestHeaderPrefix))
	if headerName == "" {
		return fmt.Errorf("request header name is required")
	}
	if len(headerName) > 256 {
		return fmt.Errorf("request header name is too long")
	}
	if httpclient.IsSensitiveHeader(headerName) {
		return fmt.Errorf("request header %q is sensitive and cannot be used in conditions", headerName)
	}

	value, ok := condition.Value.(string)
	if !ok || value == "" {
		return fmt.Errorf("condition value for request_header must be a non-empty string")
	}

	return nil
}

func filterValueToInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		vv, err := v.Int64()
		return vv, err == nil
	case float64:
		return int64(v), float64(int64(v)) == v
	default:
		return 0, false
	}
}

func filterConditionValueToInt64(condition objects.Condition) (int64, bool, error) {
	value, ok := filterValueToInt64(condition.Value)
	return value, ok, nil
}

// CreateModel creates a new model with the provided input.
func (svc *ModelService) CreateModel(ctx context.Context, input ent.CreateModelInput) (*ent.Model, error) {
	// Validate regex patterns in settings if provided
	if input.Settings != nil {
		if err := svc.validateModelSettings(input.Settings); err != nil {
			return nil, err
		}
	}

	// Check if a model with the same developer and modelId already exists
	existing, err := svc.entFromContext(ctx).Model.Query().
		Where(model.ModelID(input.ModelID)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("failed to check model existence: %w", err)
	}

	if existing != nil {
		return nil, xerrors.DuplicateNameError("model", input.ModelID)
	}

	createBuilder := svc.entFromContext(ctx).Model.Create().
		SetInput(input)

	if input.Remark != nil {
		createBuilder.SetRemark(*input.Remark)
	}

	model, err := createBuilder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create model: %w", err)
	}

	svc.invalidateModelReasoningEfforts(ctx, model.ModelID)

	return model, nil
}

// BulkCreateModels creates multiple models with the provided inputs.
func (svc *ModelService) BulkCreateModels(ctx context.Context, inputs []*ent.CreateModelInput) ([]*ent.Model, error) {
	// Check for duplicates in the input
	inputMap := make(map[string]bool)

	for _, input := range inputs {
		if input.Settings != nil {
			if err := svc.validateModelSettings(input.Settings); err != nil {
				return nil, err
			}
		}

		key := fmt.Sprintf("%s:%s", input.Developer, input.ModelID)
		if inputMap[key] {
			return nil, fmt.Errorf("duplicate model in input: developer '%s' and modelId '%s'", input.Developer, input.ModelID)
		}

		inputMap[key] = true
	}

	// Check if any models already exist
	existingModels, err := svc.entFromContext(ctx).Model.Query().
		Where(func(s *sql.Selector) {
			var predicates []*sql.Predicate
			for _, input := range inputs {
				predicates = append(predicates, sql.And(
					sql.EQ(model.FieldDeveloper, input.Developer),
					sql.EQ(model.FieldModelID, input.ModelID),
				))
			}

			s.Where(sql.Or(predicates...))
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing models: %w", err)
	}

	if len(existingModels) > 0 {
		existingKeys := lo.Map(existingModels, func(m *ent.Model, _ int) string {
			return fmt.Sprintf("%s:%s", m.Developer, m.ModelID)
		})

		return nil, fmt.Errorf("models already exist: %v", existingKeys)
	}

	// Create all models in a transaction
	bulk := make([]*ent.ModelCreate, len(inputs))
	for i, input := range inputs {
		createBuilder := svc.entFromContext(ctx).Model.Create().
			SetInput(*input)

		if input.Remark != nil {
			createBuilder.SetRemark(*input.Remark)
		}

		bulk[i] = createBuilder
	}

	models, err := svc.entFromContext(ctx).Model.CreateBulk(bulk...).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to bulk create models: %w", err)
	}

	svc.invalidateModelReasoningEfforts(ctx, lo.Map(models, func(m *ent.Model, _ int) string { return m.ModelID })...)

	return models, nil
}

// UpdateModel updates an existing model with the provided input.
func (svc *ModelService) UpdateModel(ctx context.Context, id int, input *ent.UpdateModelInput) (*ent.Model, error) {
	// Validate regex patterns in settings if provided
	if input.Settings != nil {
		if err := svc.validateModelSettings(input.Settings); err != nil {
			return nil, err
		}
	}

	// A rename leaves a stale entry under the previous model id.
	var previousModelID string

	if input.ModelID != nil {
		if ids := svc.queryModelIDs(ctx, id); len(ids) == 1 {
			previousModelID = ids[0]
		}
	}

	mut := svc.entFromContext(ctx).Model.UpdateOneID(id).
		SetNillableDeveloper(input.Developer).
		SetNillableModelID(input.ModelID).
		SetNillableType(input.Type).
		SetNillableName(input.Name).
		SetNillableGroup(input.Group).
		SetNillableStatus(input.Status).
		SetNillableIcon(input.Icon)

	if input.ModelCard != nil {
		mut.SetModelCard(input.ModelCard)
	}

	if input.Settings != nil {
		mut.SetSettings(input.Settings)
	}

	if input.Remark != nil {
		mut.SetRemark(*input.Remark)
	}

	if input.ClearRemark {
		mut.ClearRemark()
	}

	model, err := mut.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update model: %w", err)
	}

	svc.invalidateModelReasoningEfforts(ctx, previousModelID, model.ModelID)

	return model, nil
}

// UpdateModelStatus updates the status of a model.
func (svc *ModelService) UpdateModelStatus(ctx context.Context, id int, status model.Status) (*ent.Model, error) {
	model, err := svc.entFromContext(ctx).Model.UpdateOneID(id).
		SetStatus(status).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update model status: %w", err)
	}

	svc.invalidateModelReasoningEfforts(ctx, model.ModelID)

	return model, nil
}

// DeleteModel deletes a model by ID.
func (svc *ModelService) DeleteModel(ctx context.Context, id int) error {
	modelIDs := svc.queryModelIDs(ctx, id)

	if err := svc.entFromContext(ctx).Model.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("failed to delete model: %w", err)
	}

	svc.invalidateModelReasoningEfforts(ctx, modelIDs...)

	return nil
}

// BulkArchiveModels archives multiple models by their IDs.
func (svc *ModelService) BulkArchiveModels(ctx context.Context, ids []int) error {
	svc.invalidateModelReasoningEffortsByIDs(ctx, ids...)

	_, err := svc.entFromContext(ctx).Model.Update().
		Where(model.IDIn(ids...)).
		SetStatus(model.StatusArchived).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk archive models: %w", err)
	}

	return nil
}

// BulkDisableModels disables multiple models by their IDs.
func (svc *ModelService) BulkDisableModels(ctx context.Context, ids []int) error {
	svc.invalidateModelReasoningEffortsByIDs(ctx, ids...)

	_, err := svc.entFromContext(ctx).Model.Update().
		Where(model.IDIn(ids...)).
		SetStatus(model.StatusDisabled).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk disable models: %w", err)
	}

	return nil
}

// BulkEnableModels enables multiple models by their IDs.
func (svc *ModelService) BulkEnableModels(ctx context.Context, ids []int) error {
	svc.invalidateModelReasoningEffortsByIDs(ctx, ids...)

	_, err := svc.entFromContext(ctx).Model.Update().
		Where(model.IDIn(ids...)).
		SetStatus(model.StatusEnabled).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk enable models: %w", err)
	}

	return nil
}

// BulkDeleteModels deletes multiple models by their IDs.
func (svc *ModelService) BulkDeleteModels(ctx context.Context, ids []int) error {
	svc.invalidateModelReasoningEffortsByIDs(ctx, ids...)

	_, err := svc.entFromContext(ctx).Model.Delete().
		Where(model.IDIn(ids...)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to bulk delete models: %w", err)
	}

	return nil
}

// QueryModelChannelConnections queries channels and their models based on model associations.
// Results are ordered by the matching order of associations.
func (svc *ModelService) QueryModelChannelConnections(ctx context.Context, associations []*objects.ModelAssociation) ([]*ModelChannelConnection, error) {
	if len(associations) == 0 {
		return []*ModelChannelConnection{}, nil
	}

	// Query all enabled/disabled channels
	channels, err := svc.entFromContext(ctx).Channel.Query().
		Where(channel.StatusIn(channel.StatusEnabled, channel.StatusDisabled)).
		Order(channel.ByOrderingWeight(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query channels: %w", err)
	}

	if len(channels) == 0 {
		return []*ModelChannelConnection{}, nil
	}

	// Use the shared MatchAssociations function
	return MatchConnections(associations, lo.Map(channels, func(ch *ent.Channel, _ int) *Channel {
		return &Channel{Channel: ch}
	})), nil
}

// GetModelByModelID retrieves a model by its modelId and status.
func (svc *ModelService) GetModelByModelID(ctx context.Context, modelID string, status model.Status) (*ent.Model, error) {
	return svc.entFromContext(ctx).Model.Query().
		Where(
			model.ModelID(modelID),
			model.StatusEQ(status),
		).
		First(ctx)
}

const modelReasoningEffortsCacheKeyPrefix = "model:reasoning-efforts:"

func modelReasoningEffortsCacheKey(modelID string) string {
	return modelReasoningEffortsCacheKeyPrefix + modelID
}

// cachedModelReasoningEfforts reads the cache entry of a model id. The second result is
// false when the cache is unavailable (eg. a service literal without a cache).
func (svc *ModelService) cachedModelReasoningEfforts(ctx context.Context, modelID string) (xcache.Entry[[]string], bool) {
	if svc.reasoningEffortsCache == nil {
		return xcache.Entry[[]string]{}, false
	}

	entry, err := svc.reasoningEffortsCache.Get(ctx, modelReasoningEffortsCacheKey(modelID))
	if err != nil {
		return xcache.Entry[[]string]{}, false
	}

	return entry, true
}

func (svc *ModelService) setCachedModelReasoningEfforts(ctx context.Context, modelID string, entry xcache.Entry[[]string]) {
	if svc.reasoningEffortsCache == nil {
		return
	}

	if err := svc.reasoningEffortsCache.Set(ctx, modelReasoningEffortsCacheKey(modelID), entry); err != nil {
		log.Warn(ctx, "failed to cache model reasoning efforts", log.String("model_id", modelID), log.Cause(err))
	}
}

// GetModelReasoningEfforts returns the reasoning effort levels declared by the model card
// of each requested model id. Only enabled models are resolved.
//
// A present key with a nil value means the model exists but does not declare any levels
// (unknown); an absent key means the model is not in the library or is not enabled.
// Results are cached, so a hot model costs no database query; all cache misses of one
// call are resolved with a single query.
func (svc *ModelService) GetModelReasoningEfforts(ctx context.Context, modelIDs ...string) (map[string][]string, error) {
	result := make(map[string][]string, len(modelIDs))
	missing := make([]string, 0, len(modelIDs))
	requested := make(map[string]struct{}, len(modelIDs))

	for _, modelID := range modelIDs {
		if modelID == "" {
			continue
		}

		if _, ok := requested[modelID]; ok {
			continue
		}

		requested[modelID] = struct{}{}

		entry, ok := svc.cachedModelReasoningEfforts(ctx, modelID)
		if !ok || entry.IsExpired() {
			missing = append(missing, modelID)

			continue
		}

		// Negative entry: the model is known to be missing from the library.
		if entry.IsEmpty {
			continue
		}

		result[modelID] = entry.Value
	}

	if len(missing) == 0 {
		return result, nil
	}

	models, err := svc.entFromContext(ctx).Model.Query().
		Where(
			model.ModelIDIn(missing...),
			model.StatusEQ(model.StatusEnabled),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query model reasoning efforts: %w", err)
	}

	found := make(map[string]struct{}, len(models))

	for _, m := range models {
		found[m.ModelID] = struct{}{}

		var efforts []string
		if m.ModelCard != nil {
			efforts = m.ModelCard.ReasoningEfforts
		}

		result[m.ModelID] = efforts

		svc.setCachedModelReasoningEfforts(ctx, m.ModelID, *xcache.NewEntry(efforts, 0))
	}

	for _, modelID := range missing {
		if _, ok := found[modelID]; ok {
			continue
		}

		svc.setCachedModelReasoningEfforts(ctx, modelID, *xcache.NewEmptyEntry[[]string](negativeCacheTTL))
	}

	return result, nil
}

// invalidateModelReasoningEfforts drops cached reasoning effort levels for the given model ids.
func (svc *ModelService) invalidateModelReasoningEfforts(ctx context.Context, modelIDs ...string) {
	if svc.reasoningEffortsCache == nil {
		return
	}

	for _, modelID := range modelIDs {
		if modelID == "" {
			continue
		}

		if err := svc.reasoningEffortsCache.Delete(ctx, modelReasoningEffortsCacheKey(modelID)); err != nil {
			log.Warn(ctx, "failed to invalidate model reasoning efforts cache",
				log.String("model_id", modelID), log.Cause(err))
		}
	}
}

// queryModelIDs resolves model ids by primary keys, returning nil when the lookup fails.
func (svc *ModelService) queryModelIDs(ctx context.Context, ids ...int) []string {
	if len(ids) == 0 {
		return nil
	}

	models, err := svc.entFromContext(ctx).Model.Query().
		Where(model.IDIn(ids...)).
		Select(model.FieldModelID).
		All(ctx)
	if err != nil {
		log.Warn(ctx, "failed to query model ids for cache invalidation", log.Cause(err))

		return nil
	}

	return lo.Map(models, func(m *ent.Model, _ int) string { return m.ModelID })
}

// invalidateModelReasoningEffortsByIDs drops cached levels for models addressed by primary key.
func (svc *ModelService) invalidateModelReasoningEffortsByIDs(ctx context.Context, ids ...int) {
	svc.invalidateModelReasoningEfforts(ctx, svc.queryModelIDs(ctx, ids...)...)
}

// ListModels retrieves all models that have explicit Model entity configuration.
// Returns models with their status.
func (svc *ModelService) ListModels(ctx context.Context, statusIn []model.Status) ([]*ModelIdentityWithStatus, error) {
	query := svc.entFromContext(ctx).Model.Query()

	// Apply status filter if provided
	if len(statusIn) > 0 {
		query = query.Where(model.StatusIn(statusIn...))
	} else {
		// Default to enabled models only
		query = query.Where(model.StatusEQ(model.StatusEnabled))
	}

	models, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query configured models: %w", err)
	}

	// Convert to ModelIdentityWithStatus
	result := make([]*ModelIdentityWithStatus, 0, len(models))
	for _, m := range models {
		result = append(result, &ModelIdentityWithStatus{
			ID:     m.ModelID,
			Status: channel.Status(m.Status.String()),
		})
	}

	return result, nil
}

// ListEnabledModels returns all unique models across all enabled channels,
// considering model mappings, prefixes, and auto-trimmed models.
// It uses GetModelEntries to reduce code duplication.
// When QueryAllChannelModels in system settings is false, it returns configured models instead.
// If an API key is present in context and has an active profile with modelIDs configured,
// only those models will be returned.
// When HideUnroutableModelsInList is true, configured models with no capable
// endpoint on the key-scoped channels are omitted from this public list.
func (svc *ModelService) ListEnabledModels(ctx context.Context) ([]ModelFacade, error) {
	var (
		channels = svc.channelService.GetEnabledChannels()
		profile  *objects.APIKeyProfile
	)

	ctx = authz.WithScopeDecision(ctx, scopes.ScopeReadChannels)

	if apiKey, ok := contexts.GetAPIKey(ctx); ok && apiKey != nil {
		// Project-level profile filtering (upper boundary)
		if projectProfile := apiKey.Edges.Project.GetActiveProfile(); projectProfile != nil {
			if len(projectProfile.ChannelIDs) > 0 {
				channels = lo.Filter(channels, func(ch *Channel, _ int) bool {
					return lo.Contains(projectProfile.ChannelIDs, ch.ID)
				})
			}

			if len(projectProfile.ChannelTags) > 0 {
				channels = lo.Filter(channels, func(ch *Channel, _ int) bool {
					return projectProfile.MatchChannelTags(ch.Tags)
				})
			}
		}

		// Key-level profile filtering (narrows further within project scope)
		profile = apiKey.GetActiveProfile()

		if profile != nil && len(profile.ChannelIDs) > 0 {
			channels = lo.Filter(channels, func(ch *Channel, _ int) bool {
				return lo.Contains(profile.ChannelIDs, ch.ID)
			})
		}

		if profile != nil && len(profile.ChannelTags) > 0 {
			channels = lo.Filter(channels, func(ch *Channel, _ int) bool {
				return profile.MatchChannelTags(ch.Tags)
			})
		}
	}

	var allowedModelIDs []string
	if profile != nil && len(profile.ModelIDs) > 0 {
		allowedModelIDs = profile.ModelIDs
	}

	// Query configured Model entities (used in both modes)
	configuredModels, suppressedIDs, err := svc.queryConfiguredModelFacades(ctx, allowedModelIDs, channels)
	if err != nil {
		return nil, err
	}

	settings := svc.systemService.ModelSettingsOrDefault(ctx)
	if !settings.QueryAllChannelModels {
		return configuredModels, nil
	}

	// QueryAllChannelModels=true: merge configured models (higher priority) with channel models
	var (
		models    = configuredModels
		modelSet  = make(map[string]bool, len(configuredModels)+len(suppressedIDs))
		blacklist = settings.ModelBlacklistRegex
	)

	for _, m := range configuredModels {
		modelSet[m.ID] = true
	}
	// Hidden configured IDs must not reappear as channel-derived facades.
	// Routing still binds those IDs to the configured associations.
	for id := range suppressedIDs {
		modelSet[id] = true
	}

	for _, ch := range channels {
		entries := ch.GetModelEntries()

		for requestModel := range entries {
			if modelSet[requestModel] {
				continue
			}

			// Channel-derived models matching the blacklist regex are excluded.
			// Configured Model entities above are not affected. Cache the decision
			// in modelSet so the same model ID coming from another channel skips
			// the regex match.
			if blacklist != "" && xregexp.MatchString(blacklist, requestModel) {
				modelSet[requestModel] = true
				continue
			}

			modelSet[requestModel] = true

			models = append(models, ModelFacade{
				ID:          requestModel,
				DisplayName: requestModel,
				CreatedAt:   ch.CreatedAt,
				Created:     ch.CreatedAt.Unix(),
				OwnedBy:     ch.Channel.Type.String(),
			})
		}
	}

	// Apply model filtering from key profile
	if len(allowedModelIDs) > 0 {
		models = lo.Filter(models, func(m ModelFacade, _ int) bool {
			return lo.Contains(allowedModelIDs, m.ID)
		})
	}

	return models, nil
}

// queryConfiguredModelFacades queries enabled Model entities and returns them as ModelFacades
// filtered by allowed model IDs and channel associations.
// suppressedIDs are configured model IDs omitted as structurally unroutable; callers that
// merge channel-derived models must treat them as already seen so they are not resurrected.
func (svc *ModelService) queryConfiguredModelFacades(ctx context.Context, allowedModelIDs []string, channels []*Channel) ([]ModelFacade, map[string]struct{}, error) {
	query := svc.entFromContext(ctx).
		Model.
		Query().
		Where(model.StatusEQ(model.StatusEnabled))
	if len(allowedModelIDs) > 0 {
		query = query.Where(model.ModelIDIn(allowedModelIDs...))
	}

	enabledModels, err := query.All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list configured models: %w", err)
	}

	var models []ModelFacade
	suppressedIDs := make(map[string]struct{})
	systemSettings := svc.modelSettingsOrDefault(ctx)

	for _, m := range enabledModels {
		effectiveAssociations := EffectiveModelAssociations(systemSettings, m)
		connections := MatchConnections(effectiveAssociations, channels)
		if len(connections) == 0 {
			continue
		}

		if systemSettings.HideUnroutableModelsInList && !hasCapableEndpointForModel(m, connections) {
			suppressedIDs[m.ModelID] = struct{}{}
			continue
		}

		models = append(models, ModelFacade{
			ID:          m.ModelID,
			DisplayName: m.ModelID,
			CreatedAt:   m.CreatedAt,
			Created:     m.CreatedAt.Unix(),
			OwnedBy:     "configured",
		})
	}

	return models, suppressedIDs, nil
}

// CountAssociatedChannels counts the number of unique channels associated with the given model associations.
func (svc *ModelService) CountAssociatedChannels(ctx context.Context, associations []*objects.ModelAssociation) (int, error) {
	return svc.countAssociatedChannels(ctx, associations)
}

// CountModelAssociatedChannels counts associated channels after applying developer-level inherited associations.
func (svc *ModelService) CountModelAssociatedChannels(ctx context.Context, m *ent.Model) (int, error) {
	return svc.countAssociatedChannels(ctx, EffectiveModelAssociations(svc.modelSettingsOrDefault(ctx), m))
}

func (svc *ModelService) QueryUnassociatedChannels(ctx context.Context) ([]*UnassociatedChannel, error) {
	channels, err := svc.entFromContext(ctx).Channel.Query().
		Where(channel.StatusIn(channel.StatusEnabled, channel.StatusDisabled)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query channels: %w", err)
	}

	if len(channels) == 0 {
		return []*UnassociatedChannel{}, nil
	}

	models, err := svc.entFromContext(ctx).Model.Query().
		Where(model.StatusIn(model.StatusEnabled, model.StatusDisabled)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query models: %w", err)
	}

	allAssociations := make([]*objects.ModelAssociation, 0)
	systemSettings := svc.modelSettingsOrDefault(ctx)

	for _, m := range models {
		allAssociations = append(allAssociations, EffectiveModelAssociations(systemSettings, m)...)
	}

	return findUnassociatedChannels(channels, allAssociations), nil
}

func (svc *ModelService) countAssociatedChannels(ctx context.Context, associations []*objects.ModelAssociation) (int, error) {
	if len(associations) == 0 {
		return 0, nil
	}

	// Query all enabled/disabled channels
	channels, err := svc.entFromContext(ctx).Channel.Query().
		Where(channel.StatusIn(channel.StatusEnabled, channel.StatusDisabled)).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to query channels: %w", err)
	}

	if len(channels) == 0 {
		return 0, nil
	}

	connections := MatchConnections(associations, lo.Map(channels, func(ch *ent.Channel, _ int) *Channel {
		return &Channel{Channel: ch}
	}))

	connections = lo.UniqBy(connections, func(conn *ModelChannelConnection) int {
		return conn.Channel.ID
	})

	return len(connections), nil
}

func (svc *ModelService) modelSettingsOrDefault(ctx context.Context) *SystemModelSettings {
	if svc.systemService == nil {
		return lo.ToPtr(defaultModelSettings)
	}

	return svc.systemService.ModelSettingsOrDefault(ctx)
}

func findUnassociatedChannels(channels []*ent.Channel, associations []*objects.ModelAssociation) []*UnassociatedChannel {
	if len(channels) == 0 {
		return []*UnassociatedChannel{}
	}

	// Wrap channels
	channelWrappers := make([]*Channel, 0, len(channels))
	for _, ch := range channels {
		channelWrappers = append(channelWrappers, &Channel{Channel: ch})
	}

	// Use MatchAssociations to get all associated models
	connections := MatchConnections(associations, channelWrappers)

	// Build a map of associated (channelID, modelID) combinations
	associatedMap := make(map[ChannelModelKey]bool)

	for _, conn := range connections {
		for _, entry := range conn.Models {
			key := ChannelModelKey{
				ChannelID: conn.Channel.ID,
				ModelID:   entry.RequestModel,
			}
			associatedMap[key] = true
		}
	}

	// Check each channel for unassociated models
	result := make([]*UnassociatedChannel, 0)

	for _, ch := range channels {
		channelWrapper := &Channel{Channel: ch}
		entries := channelWrapper.GetModelEntries()

		unassociatedModels := make([]string, 0)

		for modelID := range entries {
			key := ChannelModelKey{
				ChannelID: ch.ID,
				ModelID:   modelID,
			}
			if !associatedMap[key] {
				unassociatedModels = append(unassociatedModels, modelID)
			}
		}

		if len(unassociatedModels) > 0 {
			result = append(result, &UnassociatedChannel{
				Channel: ch,
				Models:  unassociatedModels,
			})
		}
	}

	return result
}

type ModelIdentify struct {
	ID string `json:"id"`
}

type UnassociatedChannel struct {
	Channel *ent.Channel `json:"channel"`
	Models  []string     `json:"models"`
}

type ModelFacade struct {
	ID string `json:"id"`
	// Display name, for user-friendly display from anthropic API.
	DisplayName string `json:"display_name"`
	// Created time in seconds.
	Created int64 `json:"created"`
	// Created time in time.Time.
	CreatedAt time.Time `json:"created_at"`
	// Owned by
	OwnedBy string `json:"owned_by"`
}
