package protocol

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONConverter_FromHTTP(t *testing.T) {
	converter := NewJSONConverter("/v1")

	body := []byte(`{"format":"text"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/pdf/convert", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", "tenant-1")
	req.Header.Set("X-Trace-ID", "trace-1")
	req.Header.Set("X-User-ID", "user-1")

	converted, err := converter.FromHTTP(req)
	if err != nil {
		t.Fatalf("FromHTTP failed: %v", err)
	}

	if converted.Engine != "pdf" {
		t.Errorf("expected pdf, got %s", converted.Engine)
	}
	if converted.Capability != "convert" {
		t.Errorf("expected convert, got %s", converted.Capability)
	}
	if converted.TenantID != "tenant-1" {
		t.Errorf("expected tenant-1, got %s", converted.TenantID)
	}
	if converted.TraceID != "trace-1" {
		t.Errorf("expected trace-1, got %s", converted.TraceID)
	}
	if string(converted.Payload) != `{"format":"text"}` {
		t.Errorf("unexpected payload: %s", converted.Payload)
	}
}

func TestJSONConverter_FromHTTP_InvalidPath(t *testing.T) {
	converter := NewJSONConverter("/v1")

	req := httptest.NewRequest(http.MethodPost, "/v1/pdf", nil)
	_, err := converter.FromHTTP(req)
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestJSONConverter_FromHTTP_WithVersion(t *testing.T) {
	converter := NewJSONConverter("")

	req := httptest.NewRequest(http.MethodPost, "/pdf/v2/convert", nil)
	converted, err := converter.FromHTTP(req)
	if err != nil {
		t.Fatalf("FromHTTP failed: %v", err)
	}

	if converted.Version != "v2" {
		t.Errorf("expected v2, got %s", converted.Version)
	}
	if converted.Capability != "convert" {
		t.Errorf("expected convert, got %s", converted.Capability)
	}
}

func TestJSONConverter_ToHTTP(t *testing.T) {
	converter := NewJSONConverter("")

	recorder := httptest.NewRecorder()
	resp := &Response{
		Status:      http.StatusOK,
		Payload:     []byte(`{"result":"ok"}`),
		ContentType: "application/json",
		Headers:     map[string]string{"X-Custom": "value"},
		Duration:    5,
	}

	err := converter.ToHTTP(recorder, resp)
	if err != nil {
		t.Fatalf("ToHTTP failed: %v", err)
	}

	if recorder.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", recorder.Code)
	}
	if recorder.Header().Get("X-Custom") != "value" {
		t.Error("expected custom header")
	}
	if recorder.Body.String() != `{"result":"ok"}` {
		t.Errorf("unexpected body: %s", recorder.Body.String())
	}
}

func TestErrorResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	ErrorResponse(recorder, http.StatusNotFound, "NOT_FOUND", "resource not found", "trace-1")

	if recorder.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", recorder.Code)
	}
	if recorder.Header().Get("X-Trace-ID") != "trace-1" {
		t.Error("expected trace header")
	}
	if recorder.Body.String() == "" {
		t.Error("expected error body")
	}
}

func TestGRPCConverter_FromGRPC(t *testing.T) {
	converter := NewGRPCConverter()

	data := []byte(`{
		"engine": "storage",
		"capability": "put",
		"version": "1.0.0",
		"payload": {"bucket": "test", "key": "file.txt"}
	}`)

	req, err := converter.FromGRPC(data)
	if err != nil {
		t.Fatalf("FromGRPC failed: %v", err)
	}

	if req.Engine != "storage" {
		t.Errorf("expected storage, got %s", req.Engine)
	}
	if req.Capability != "put" {
		t.Errorf("expected put, got %s", req.Capability)
	}
	if req.Version != "1.0.0" {
		t.Errorf("expected 1.0.0, got %s", req.Version)
	}
	if len(req.Payload) == 0 {
		t.Error("expected payload")
	}
}

func TestGRPCConverter_Invalid(t *testing.T) {
	converter := NewGRPCConverter()

	_, err := converter.FromGRPC([]byte(`{"engine":""}`))
	if err == nil {
		t.Error("expected error for missing engine")
	}

	_, err = converter.FromGRPC([]byte(`invalid json`))
	if err == nil {
		t.Error("expected error for invalid json")
	}
}
