package httpapi

import (
	"context"
	"fmt"

	chimw "github.com/go-chi/chi/v5/middleware"
)

func (s *Server) recordShipDelivery(
	ctx context.Context,
	projectID int64,
	actor, substage, stepKind, idempotencyKey string,
	payload, result map[string]any,
) {
	if s.canonical == nil || projectID <= 0 {
		return
	}
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("%s-%s", stepKind, chimw.GetReqID(ctx))
	}
	_ = s.canonical.RecordShipDelivery(ctx, projectID, substage, stepKind, idempotencyKey, actor, payload, result)
	_, _ = s.canonical.EnqueueProjection(ctx, projectID, "ship", stepKind, map[string]any{
		"substage": substage,
		"payload":  payload,
		"result":   result,
	}, chimw.GetReqID(ctx))
	_ = s.canonical.RefreshEligibility(ctx, projectID, actor)
}
