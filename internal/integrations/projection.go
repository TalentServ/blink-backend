package integrations

import (
	"context"

	"github.com/go-chi/chi/v5/middleware"
)

type projectionEnqueueFunc func(ctx context.Context, projectID int64, provider, actionType string, payload map[string]any, correlationID string) (int64, error)

func (s *Service) BindProjectionEnqueue(fn projectionEnqueueFunc) {
	s.projectionEnqueue = fn
}

func (s *Service) recordProjection(ctx context.Context, projectID int64, provider, actionType string, payload map[string]any) {
	if s.projectionEnqueue == nil || projectID <= 0 {
		return
	}
	correlationID := middleware.GetReqID(ctx)
	_, _ = s.projectionEnqueue(ctx, projectID, provider, actionType, payload, correlationID)
}
