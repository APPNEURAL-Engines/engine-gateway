// Package connectgateway adapts grpcserver.Server onto the Connect
// protocol (connectrpc.com/connect), which -- unlike plain
// google.golang.org/grpc -- speaks Connect, gRPC, and gRPC-Web from a
// single http.Handler. That's what lets browser clients (and any plain
// HTTP client, including curl) reach the gateway without a separate
// gRPC-Web proxy in front of it.
//
// This package deliberately does not reimplement auth, routing, rate
// limiting, or circuit breaking: it delegates every call to an existing
// grpcserver.Server, so all three front doors (HTTP/JSON, gRPC, Connect)
// enforce identical policy from the same instances. The only real work
// here is bridging two different "how do I read a header / report an
// error" conventions: connect.Request.Header() -> grpc's incoming
// metadata.MD (which grpcserver.Server.authenticate reads), and
// google.golang.org/grpc/status errors -> connect.Error.
package connectgateway

import (
	"context"
	"errors"
	"net/http"
	"strings"

	enginev1 "github.com/APPNEURAL-Engines/contracts/gen/go/engine"
	"github.com/APPNEURAL-Engines/contracts/gen/go/engine/enginev1connect"
	"github.com/APPNEURAL-Engines/engine-gateway/grpcserver"

	"connectrpc.com/connect"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Handler implements enginev1connect.EngineServiceHandler by delegating to
// a grpcserver.Server.
type Handler struct {
	grpc *grpcserver.Server
}

// NewHTTPHandler builds the mountable (path, http.Handler) pair for a
// standard library mux, e.g.:
//
//	path, handler := connectgateway.NewHTTPHandler(grpcServer)
//	mux.Handle(path, handler)
func NewHTTPHandler(grpc *grpcserver.Server, opts ...connect.HandlerOption) (string, http.Handler) {
	return enginev1connect.NewEngineServiceHandler(&Handler{grpc: grpc}, opts...)
}

// bridgeHeaders copies HTTP headers (how connect.Request exposes them)
// into gRPC incoming metadata (how grpcserver.Server.authenticate reads
// them), so the exact same auth code path serves all three protocols.
func bridgeHeaders(ctx context.Context, header http.Header) context.Context {
	md := make(metadata.MD, len(header))
	for key, values := range header {
		md[strings.ToLower(key)] = values
	}
	return metadata.NewIncomingContext(ctx, md)
}

// translateError converts a google.golang.org/grpc/status error (what
// every grpcserver.Server method returns) into a connect.Error. Connect's
// error codes are numerically identical to the standard gRPC status codes
// by design, so the conversion is a direct cast, not a lookup table.
func translateError(err error) error {
	if err == nil {
		return nil
	}
	return connect.NewError(connect.Code(status.Code(err)), errors.New(status.Convert(err).Message()))
}

// Execute implements enginev1connect.EngineServiceHandler.
func (h *Handler) Execute(ctx context.Context, req *connect.Request[enginev1.ExecuteRequest]) (*connect.Response[enginev1.ExecuteResponse], error) {
	resp, err := h.grpc.Execute(bridgeHeaders(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, translateError(err)
	}
	return connect.NewResponse(resp), nil
}

// GetHealth implements enginev1connect.EngineServiceHandler.
func (h *Handler) GetHealth(ctx context.Context, req *connect.Request[enginev1.GetHealthRequest]) (*connect.Response[enginev1.GetHealthResponse], error) {
	resp, err := h.grpc.GetHealth(bridgeHeaders(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, translateError(err)
	}
	return connect.NewResponse(resp), nil
}

// ListEngines implements enginev1connect.EngineServiceHandler.
func (h *Handler) ListEngines(ctx context.Context, req *connect.Request[enginev1.ListEnginesRequest]) (*connect.Response[enginev1.ListEnginesResponse], error) {
	resp, err := h.grpc.ListEngines(bridgeHeaders(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, translateError(err)
	}
	return connect.NewResponse(resp), nil
}

// GetEngine implements enginev1connect.EngineServiceHandler.
func (h *Handler) GetEngine(ctx context.Context, req *connect.Request[enginev1.GetEngineRequest]) (*connect.Response[enginev1.GetEngineResponse], error) {
	resp, err := h.grpc.GetEngine(bridgeHeaders(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, translateError(err)
	}
	return connect.NewResponse(resp), nil
}
