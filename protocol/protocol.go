package protocol

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Request represents a gateway request after protocol conversion.
type Request struct {
	// Engine is the target engine name.
	Engine string

	// Capability is the operation to perform.
	Capability string

	// Version is the version constraint.
	Version string

	// Payload is the serialized request data.
	Payload []byte

	// ContentType is the payload content type.
	ContentType string

	// Headers are the request headers.
	Headers map[string]string

	// TenantID is the resolved tenant.
	TenantID string

	// UserID is the authenticated user.
	UserID string

	// TraceID is the distributed trace identifier.
	TraceID string

	// Timeout is the request timeout.
	Timeout time.Duration
}

// Response represents a gateway response.
type Response struct {
	// Status is the HTTP-like status code.
	Status int

	// Payload is the serialized response data.
	Payload []byte

	// ContentType is the payload content type.
	ContentType string

	// Headers are the response headers.
	Headers map[string]string

	// Duration is the processing duration.
	Duration time.Duration
}

// Converter converts between protocols (HTTP, gRPC).
type Converter interface {
	// FromHTTP converts an HTTP request to a gateway request.
	FromHTTP(r *http.Request) (*Request, error)

	// ToHTTP converts a gateway response to an HTTP response.
	ToHTTP(w http.ResponseWriter, resp *Response) error
}

// JSONConverter converts JSON-based HTTP requests.
type JSONConverter struct {
	// PathPrefix is stripped from the URL path.
	PathPrefix string
}

// NewJSONConverter creates a JSON converter.
func NewJSONConverter(pathPrefix string) *JSONConverter {
	return &JSONConverter{PathPrefix: pathPrefix}
}

// FromHTTP converts an HTTP request to a gateway request.
func (c *JSONConverter) FromHTTP(r *http.Request) (*Request, error) {
	// Parse path: /v1/{engine}/{capability}
	path := r.URL.Path
	if c.PathPrefix != "" {
		path = strings.TrimPrefix(path, c.PathPrefix)
	}
	path = strings.Trim(path, "/")

	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return nil, errors.New("invalid path, expected /{engine}/{capability}")
	}

	req := &Request{
		Engine:      parts[0],
		Capability:  parts[1],
		ContentType: "application/json",
		Headers:     make(map[string]string),
	}

	// Parse version constraint from path (e.g., /pdf/v2/convert)
	if len(parts) >= 3 && strings.HasPrefix(parts[1], "v") {
		req.Version = parts[1]
		req.Capability = parts[2]
	}

	// Copy headers
	for k, v := range r.Header {
		if len(v) > 0 {
			req.Headers[k] = v[0]
		}
	}

	// Extract context headers
	req.TenantID = firstNonEmpty(
		r.Header.Get("X-Tenant-ID"),
		req.Headers["x-tenant-id"],
	)
	req.UserID = r.Header.Get("X-User-ID")
	req.TraceID = firstNonEmpty(
		r.Header.Get("X-Trace-ID"),
		r.Header.Get("Traceparent"),
	)

	// Extract timeout
	if timeoutStr := r.Header.Get("X-Timeout"); timeoutStr != "" {
		if timeout, err := time.ParseDuration(timeoutStr); err == nil {
			req.Timeout = timeout
		}
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	req.Payload = body

	return req, nil
}

// ToHTTP converts a gateway response to an HTTP response.
func (c *JSONConverter) ToHTTP(w http.ResponseWriter, resp *Response) error {
	// Set headers
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}

	w.Header().Set("Content-Type", resp.ContentType)
	w.Header().Set("X-Duration-Ms", itoa(int64(resp.Duration.Milliseconds())))
	w.WriteHeader(resp.Status)

	if resp.Payload != nil {
		_, err := w.Write(resp.Payload)
		return err
	}
	return nil
}

// ErrorResponse writes an error response.
func ErrorResponse(w http.ResponseWriter, status int, code string, message string, traceID string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Trace-ID", traceID)
	w.WriteHeader(status)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":     code,
			"message":  message,
			"trace_id": traceID,
		},
	})
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// itoa converts an integer to a string.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// grpcRequest represents a gRPC-style request.
type grpcRequest struct {
	Engine     string            `json:"engine"`
	Capability string            `json:"capability"`
	Version    string            `json:"version,omitempty"`
	Payload    json.RawMessage   `json:"payload"`
	Headers    map[string]string `json:"headers,omitempty"`
}

// GRPCConverter converts gRPC-style JSON requests.
type GRPCConverter struct{}

// NewGRPCConverter creates a gRPC converter.
func NewGRPCConverter() *GRPCConverter {
	return &GRPCConverter{}
}

// FromGRPC converts a gRPC envelope to a gateway request.
func (c *GRPCConverter) FromGRPC(data []byte) (*Request, error) {
	var grpcReq grpcRequest
	if err := json.Unmarshal(data, &grpcReq); err != nil {
		return nil, err
	}

	if grpcReq.Engine == "" || grpcReq.Capability == "" {
		return nil, errors.New("engine and capability are required")
	}

	payload, err := json.Marshal(grpcReq.Payload)
	if err != nil {
		return nil, err
	}

	return &Request{
		Engine:      grpcReq.Engine,
		Capability:  grpcReq.Capability,
		Version:     grpcReq.Version,
		Payload:     payload,
		ContentType: "application/json",
		Headers:     grpcReq.Headers,
	}, nil
}
