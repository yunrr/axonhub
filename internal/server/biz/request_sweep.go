package biz

import (
	"context"
	"fmt"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent/request"
	"github.com/looplj/axonhub/internal/ent/requestexecution"
	"github.com/looplj/axonhub/internal/log"
)

const staleRequestSweepReason = "stale-request-sweep"

// SweepStaleRequests fails requests and executions left in flight by a previous
// server run, for example a restart while a stream was still processing. The
// in-memory live buffers they depended on are gone with the old process, so
// they would otherwise stay "processing" forever: the request list keeps
// polling them and the detail page can never render content.
//
// This assumes a single server instance per database: at startup nothing can
// be in flight in this process, but a shared-database deployment would also
// sweep records still being processed by another instance.
func (s *RequestService) SweepStaleRequests(ctx context.Context) error {
	return authz.RunWithSystemBypassVoid(ctx, staleRequestSweepReason, func(bypassCtx context.Context) error {
		return s.RunInTransaction(bypassCtx, s.sweepStaleRequestsInTx)
	})
}

func (s *RequestService) sweepStaleRequestsInTx(ctx context.Context) error {
	db := s.entFromContext(ctx)

	requests, err := db.Request.Update().
		Where(request.StatusIn(request.StatusPending, request.StatusProcessing)).
		SetStatus(request.StatusFailed).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to fail stale requests: %w", err)
	}

	executions, err := db.RequestExecution.Update().
		Where(requestexecution.StatusIn(requestexecution.StatusPending, requestexecution.StatusProcessing)).
		SetStatus(requestexecution.StatusFailed).
		SetErrorMessage("interrupted by server restart").
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to fail stale request executions: %w", err)
	}

	if requests > 0 || executions > 0 {
		log.Warn(ctx, "failed stale in-flight records left by a previous run",
			log.Int("requests", requests),
			log.Int("executions", executions))
	}

	return nil
}
