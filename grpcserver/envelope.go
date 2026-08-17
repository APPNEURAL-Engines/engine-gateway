package grpcserver

import (
	"encoding/json"
	"net/http"

	"google.golang.org/grpc/codes"
)

// Every real HTTP engine adapter (pdf-engine, storage-engine, rule-engine)
// speaks the same JSON wire envelope: {"data": ...} on success (HTTP 2xx),
// {"error": {"code", "message", "details"}} on failure (HTTP 4xx/5xx). That
// convention only reaches HTTP callers for free — a plain HTTP proxy just
// forwards status/body through unchanged. gRPC has no such freebie: an
// ExecuteResponse always comes back as a normal, non-error RPC unless this
// code explicitly turns a failed HTTP response into a gRPC error. Without
// it, every gRPC/SDK caller sees Status.Success: true and has to notice an
// "error" key buried in the payload itself to find out a call failed.
type wireEnvelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

// unwrapPayload extracts the engine's actual response data from an
// adapter's {"data": ...} envelope. If the payload doesn't parse as that
// envelope (an engine not following the convention, or a non-JSON
// response), it's returned unchanged rather than treated as an error --
// this translation is a best-effort convenience, not a hard requirement
// every engine must satisfy.
func unwrapPayload(payload []byte) []byte {
	var env wireEnvelope
	if err := json.Unmarshal(payload, &env); err != nil || len(env.Data) == 0 {
		return payload
	}
	return env.Data
}

// httpErrorToGRPC converts a failed HTTP response (status >= 400) from an
// engine adapter into a gRPC (code, message) pair, parsing the adapter's
// {"error": {...}} envelope for the message when present.
func httpErrorToGRPC(httpStatus int, payload []byte) (codes.Code, string) {
	message := http.StatusText(httpStatus)
	var env wireEnvelope
	if err := json.Unmarshal(payload, &env); err == nil && env.Error != nil && env.Error.Message != "" {
		message = env.Error.Message
		if env.Error.Code != "" {
			message = env.Error.Code + ": " + message
		}
	}
	return grpcCodeForHTTPStatus(httpStatus), message
}

func grpcCodeForHTTPStatus(httpStatus int) codes.Code {
	switch httpStatus {
	case http.StatusBadRequest:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusNotFound:
		return codes.NotFound
	case http.StatusRequestTimeout:
		return codes.DeadlineExceeded
	case http.StatusConflict:
		return codes.Aborted
	case http.StatusTooManyRequests, http.StatusRequestEntityTooLarge:
		return codes.ResourceExhausted
	case http.StatusNotImplemented:
		return codes.Unimplemented
	case http.StatusServiceUnavailable, http.StatusBadGateway:
		return codes.Unavailable
	}
	switch {
	case httpStatus >= 400 && httpStatus < 500:
		return codes.FailedPrecondition
	case httpStatus >= 500:
		return codes.Internal
	default:
		return codes.Unknown
	}
}
