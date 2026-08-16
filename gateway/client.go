package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/APPNEURAL-Engines/engine-gateway/protocol"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"
)

// HTTPEngineClient forwards requests to engines over HTTP.
type HTTPEngineClient struct {
	client *http.Client
}

// NewHTTPEngineClient creates an HTTP engine client.
func NewHTTPEngineClient(timeout time.Duration) *HTTPEngineClient {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &HTTPEngineClient{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Forward sends a request to the engine endpoint.
func (c *HTTPEngineClient) Forward(ctx context.Context, endpoint *routing.EngineEndpoint, req *protocol.Request) (*protocol.Response, error) {
	// Build URL: {address}/{capability}
	url := endpoint.Address
	if endpoint.Protocol == "http" || endpoint.Protocol == "" {
		url = ensureScheme(endpoint.Address)
	}
	url = strings.TrimRight(url, "/") + "/" + req.Capability

	// Build request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Payload))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", req.ContentType)
	if req.TenantID != "" {
		httpReq.Header.Set("X-Tenant-ID", req.TenantID)
	}
	if req.UserID != "" {
		httpReq.Header.Set("X-User-ID", req.UserID)
	}
	if req.TraceID != "" {
		httpReq.Header.Set("X-Trace-ID", req.TraceID)
	}

	// Execute
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return &protocol.Response{
		Status:      resp.StatusCode,
		Payload:     body,
		ContentType: resp.Header.Get("Content-Type"),
		Headers:     make(map[string]string),
	}, nil
}

// ensureScheme adds http:// if no scheme is present.
func ensureScheme(address string) string {
	if len(address) >= 7 && (address[:7] == "http://" || address[:8] == "https://") {
		return address
	}
	return "http://" + address
}
