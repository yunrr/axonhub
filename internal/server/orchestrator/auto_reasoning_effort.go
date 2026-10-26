package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/transformer"
)

// supportedAutoReasoningEfforts is the unified reasoning effort vocabulary (llm/reasoning.go).
// A model card that declares its own levels narrows this set for that model; models without
// declared levels accept every value here so channel-only models keep working.
var supportedAutoReasoningEfforts = map[string]struct{}{
	llm.ReasoningEffortNone:    {},
	llm.ReasoningEffortMinimal: {},
	llm.ReasoningEffortLow:     {},
	llm.ReasoningEffortMedium:  {},
	llm.ReasoningEffortHigh:    {},
	llm.ReasoningEffortXHigh:   {},
	llm.ReasoningEffortMax:     {},
}

func applyAutoReasoningEffort(systemService *biz.SystemService, modelService *biz.ModelService) pipeline.Middleware {
	return &autoReasoningEffortMiddleware{
		systemService: systemService,
		modelService:  modelService,
	}
}

type autoReasoningEffortMiddleware struct {
	pipeline.DummyMiddleware

	systemService *biz.SystemService
	modelService  *biz.ModelService
}

func (m *autoReasoningEffortMiddleware) Name() string {
	return "auto-reasoning-effort"
}

func (m *autoReasoningEffortMiddleware) OnInboundLlmRequest(ctx context.Context, llmRequest *llm.Request) (*llm.Request, error) {
	if llmRequest == nil || llmRequest.Model == "" {
		return llmRequest, nil
	}

	settings := m.systemService.ModelSettingsOrDefault(ctx)
	if !settings.AutoReasoningEffort {
		return llmRequest, nil
	}

	originalModel := llmRequest.Model

	baseModel, reasoningEffort, ok := splitAutoReasoningEffortModel(originalModel)
	if !ok {
		return llmRequest, nil
	}

	apply, err := m.resolveAutoReasoningEffort(ctx, originalModel, baseModel, reasoningEffort)
	if err != nil {
		return nil, err
	}

	if !apply {
		return llmRequest, nil
	}

	llmRequest.Model = baseModel
	llmRequest.ReasoningEffort = reasoningEffort

	log.Debug(ctx, "applied auto reasoning effort",
		log.String("original_model", originalModel),
		log.String("normalized_model", baseModel),
		log.String("reasoning_effort", reasoningEffort),
	)

	return llmRequest, nil
}

// resolveAutoReasoningEffort reports whether the effort suffix should be applied, and
// rejects suffixes the base model does not accept.
//
// The model card is authoritative when it declares levels. Without a configured base model,
// or when the card declares no levels, the unified vocabulary is accepted as-is.
func (m *autoReasoningEffortMiddleware) resolveAutoReasoningEffort(
	ctx context.Context,
	requestedModel string,
	baseModel string,
	effort string,
) (bool, error) {
	if m.modelService == nil {
		return true, nil
	}

	effortsByModel, err := m.modelService.GetModelReasoningEfforts(ctx, requestedModel, baseModel)
	if err != nil {
		return false, fmt.Errorf("failed to resolve model reasoning efforts: %w", err)
	}

	// An exact model library entry wins: the requested name is a real model, not a
	// suffixed one (eg. "qwen3.7-max").
	if _, ok := effortsByModel[requestedModel]; ok {
		return false, nil
	}

	declared, ok := effortsByModel[baseModel]
	if !ok || len(declared) == 0 {
		return true, nil
	}

	if !slices.ContainsFunc(declared, func(level string) bool { return strings.EqualFold(level, effort) }) {
		return false, fmt.Errorf("%w: model %q does not support reasoning effort %q (supported: %s)",
			transformer.ErrInvalidRequest, baseModel, effort, strings.Join(declared, ", "))
	}

	return true, nil
}

func splitAutoReasoningEffortModel(model string) (baseModel string, reasoningEffort string, ok bool) {
	lastDash := strings.LastIndex(model, "-")
	if lastDash <= 0 || lastDash == len(model)-1 {
		return "", "", false
	}

	effort := strings.ToLower(model[lastDash+1:])
	if _, supported := supportedAutoReasoningEfforts[effort]; !supported {
		return "", "", false
	}

	if effort == "max" && isQwenMaxModel(model) {
		return "", "", false
	}

	base := model[:lastDash]
	if base == "" {
		return "", "", false
	}

	return base, effort, true
}

func isQwenMaxModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if !strings.HasSuffix(normalized, "-max") {
		return false
	}

	if lastSlash := strings.LastIndex(normalized, "/"); lastSlash >= 0 {
		normalized = normalized[lastSlash+1:]
	}

	return strings.HasPrefix(normalized, "qwen")
}
