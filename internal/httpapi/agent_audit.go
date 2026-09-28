package httpapi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
)

func (s *Server) noteAgentRun(ctx context.Context, r contextCarrier, projectID int64, command string, raw json.RawMessage) {
	if s.canonical == nil || projectID <= 0 || command == "" {
		return
	}
	model := strings.TrimSpace(r.HeaderGet("X-Blink-AI-Model"))
	inDigest := canonical.DigestBytes(raw)
	_ = s.canonical.RecordAIRun(ctx, projectID, command, model, inDigest, inDigest, "")
}

type contextCarrier interface {
	HeaderGet(key string) string
}

type httpRequestCarrier struct{ h func(string) string }

func (h httpRequestCarrier) HeaderGet(key string) string { return h.h(key) }

func requestCarrier(getHeader func(string) string) contextCarrier {
	return httpRequestCarrier{h: getHeader}
}
