package logs

import (
	"context"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor returns a gRPC unary server interceptor that logs
// request details with structured logging and integrates with OpenTelemetry tracing.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()

		// Call the handler
		resp, err := handler(ctx, req)

		latency := time.Since(start)
		code := codes.OK
		if err != nil {
			if st, ok := status.FromError(err); ok {
				code = st.Code()
			}
		}

		// Log the request
		requestFields := map[string]interface{}{
			"method":  info.FullMethod,
			"service": extractService(info.FullMethod),
		}
		if correlationID := correlation.FromContext(ctx); correlationID != "" {
			requestFields["correlationID"] = correlationID
		}

		responseFields := map[string]interface{}{
			"code":      code.String(),
			"latencyMs": latency.Milliseconds(),
			"error":     err != nil,
		}

		l := L()
		switch code {
		case codes.OK:
			l.Infow("grpc request", "request", requestFields, "response", responseFields)
		case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists:
			l.Warnw("grpc request", "request", requestFields, "response", responseFields)
		default:
			l.Errorw("grpc request", "request", requestFields, "response", responseFields, "error", err)
		}

		return resp, err
	}
}

// extractService extracts service name from full method name
// "/trip.TripService/CreateTrip" -> "TripService"
func extractService(fullMethod string) string {
	if len(fullMethod) > 0 && fullMethod[0] == '/' {
		fullMethod = fullMethod[1:]
	}

	for i, ch := range fullMethod {
		if ch == '/' {
			if i > 0 {
				servicePart := fullMethod[:i]
				// Find the last dot to get service name
				for j := len(servicePart) - 1; j >= 0; j-- {
					if servicePart[j] == '.' {
						return servicePart[j+1:]
					}
				}
				return servicePart
			}
		}
	}
	return "unknown"
}
