package telemetry

import (
	"context"
	"time"

	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func BuildQueryResponse(ctx context.Context, store Store, req *agentv1.TelemetryQueryRequest, now time.Time) (*agentv1.TelemetryQueryResponse, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if req.SinceUnix <= 0 && req.Cursor == "" {
		req.SinceUnix = now.Add(-1 * time.Hour).Unix()
	}
	if req.SinceUnix > 0 {
		since := time.Unix(req.SinceUnix, 0).UTC()
		if now.Sub(since) > 24*time.Hour+time.Minute {
			return nil, status.Error(codes.InvalidArgument, "telemetry query window cannot exceed 24 hours")
		}
	}
	since := time.Unix(req.SinceUnix, 0).UTC()
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	return store.QueryBoundedTelemetry(ctx, req.ProjectID, req.ServiceID, since, now, req.IncludeSummary, req.IncludeServices, req.IncludeLogs, limit, req.Cursor)
}
