package biz

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/enttest"
	"github.com/looplj/axonhub/internal/ent/request"
	"github.com/looplj/axonhub/internal/ent/requestexecution"
	"github.com/looplj/axonhub/internal/objects"
)

func TestRequestService_SweepStaleRequests(t *testing.T) {
	client := enttest.NewEntClient(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	// The bypass covers test fixture creation; the sweep itself applies its own
	// system bypass.
	ctx := authz.WithTestBypass(ent.NewContext(context.Background(), client))

	project, err := client.Project.Create().SetName("p1").SetDescription("d").Save(ctx)
	require.NoError(t, err)

	newRequest := func(status request.Status) *ent.Request {
		req, err := client.Request.Create().
			SetProjectID(project.ID).
			SetModelID("m1").
			SetFormat("openai/chat_completions").
			SetSource("api").
			SetStatus(status).
			SetStream(true).
			SetRequestHeaders(objects.JSONRawMessage(`{}`)).
			SetRequestBody(objects.JSONRawMessage(`{}`)).
			Save(ctx)
		require.NoError(t, err)
		return req
	}

	stuck := newRequest(request.StatusProcessing)
	pending := newRequest(request.StatusPending)
	finished := newRequest(request.StatusCompleted)

	newExecution := func(requestID int, status requestexecution.Status) *ent.RequestExecution {
		exec, err := client.RequestExecution.Create().
			SetProjectID(project.ID).
			SetRequestID(requestID).
			SetModelID("m1").
			SetRequestBody(objects.JSONRawMessage(`{}`)).
			SetStatus(status).
			Save(ctx)
		require.NoError(t, err)
		return exec
	}

	stuckExec := newExecution(stuck.ID, requestexecution.StatusProcessing)
	pendingExec := newExecution(pending.ID, requestexecution.StatusPending)
	finishedExec := newExecution(finished.ID, requestexecution.StatusCompleted)

	service := &RequestService{AbstractService: &AbstractService{db: client}}
	require.NoError(t, service.SweepStaleRequests(ctx))

	swept, err := client.Request.Get(ctx, stuck.ID)
	require.NoError(t, err)
	require.Equal(t, request.StatusFailed, swept.Status)

	sweptPending, err := client.Request.Get(ctx, pending.ID)
	require.NoError(t, err)
	require.Equal(t, request.StatusFailed, sweptPending.Status)

	untouched, err := client.Request.Get(ctx, finished.ID)
	require.NoError(t, err)
	require.Equal(t, request.StatusCompleted, untouched.Status)

	sweptExec, err := client.RequestExecution.Get(ctx, stuckExec.ID)
	require.NoError(t, err)
	require.Equal(t, requestexecution.StatusFailed, sweptExec.Status)
	require.Equal(t, "interrupted by server restart", sweptExec.ErrorMessage)

	sweptPendingExec, err := client.RequestExecution.Get(ctx, pendingExec.ID)
	require.NoError(t, err)
	require.Equal(t, requestexecution.StatusFailed, sweptPendingExec.Status)

	untouchedExec, err := client.RequestExecution.Get(ctx, finishedExec.ID)
	require.NoError(t, err)
	require.Equal(t, requestexecution.StatusCompleted, untouchedExec.Status)
}
