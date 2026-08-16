package telemetry

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStdLogger_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	logger := NewStdLogger(LevelError)
	logger.Debug("debug msg", nil)
	logger.Info("info msg", nil)
	logger.Warn("warn msg", nil)
	logger.Error("error msg", errors.New("boom"), map[string]interface{}{"k": "v"})

	out := buf.String()
	if strings.Contains(out, "debug msg") || strings.Contains(out, "info msg") || strings.Contains(out, "warn msg") {
		t.Error("lower-level messages should be filtered at LevelError")
	}
	if !strings.Contains(out, "error msg") {
		t.Error("expected error message in output")
	}
	if !strings.Contains(out, "boom") {
		t.Error("expected error detail in output")
	}
	if !strings.Contains(out, "k=v") {
		t.Error("expected fields in output")
	}
}

func TestStdLogger_InfoIncluded(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	logger := NewStdLogger(LevelInfo)
	logger.Debug("debug msg", nil)
	logger.Info("info msg", nil)

	out := buf.String()
	if strings.Contains(out, "debug msg") {
		t.Error("debug should be filtered at LevelInfo")
	}
	if !strings.Contains(out, "info msg") {
		t.Error("expected info message in output")
	}
}

func TestMetrics_RecordRequest(t *testing.T) {
	m := NewMetrics()
	m.RecordRequest("pdf", true, 10*time.Millisecond)
	m.RecordRequest("pdf", false, 20*time.Millisecond)
	m.RecordRequest("storage", true, 5*time.Millisecond)

	if m.RequestCount != 3 {
		t.Errorf("expected 3 requests, got %d", m.RequestCount)
	}
	if m.SuccessCount != 2 {
		t.Errorf("expected 2 successes, got %d", m.SuccessCount)
	}
	if m.ErrorCount != 1 {
		t.Errorf("expected 1 error, got %d", m.ErrorCount)
	}
	if m.TotalLatency != 35*time.Millisecond {
		t.Errorf("expected 35ms latency, got %s", m.TotalLatency)
	}

	em := m.PerEngine["pdf"]
	if em == nil {
		t.Fatal("expected pdf engine metrics")
	}
	if em.RequestCount != 2 || em.SuccessCount != 1 || em.ErrorCount != 1 {
		t.Errorf("unexpected pdf metrics: %+v", em)
	}
}

func TestMetrics_RecordTimeout(t *testing.T) {
	m := NewMetrics()
	m.RecordTimeout()
	if m.TimeoutCount != 1 {
		t.Errorf("expected 1 timeout, got %d", m.TimeoutCount)
	}
}

func TestMetrics_Snapshot(t *testing.T) {
	m := NewMetrics()
	m.RecordRequest("pdf", true, 10*time.Millisecond)

	snap := m.Snapshot()
	if snap.RequestCount != 1 || snap.SuccessCount != 1 {
		t.Errorf("unexpected snapshot: %+v", snap)
	}

	// Mutating the original should not affect the snapshot
	m.RecordRequest("pdf", false, 5*time.Millisecond)
	if snap.RequestCount != 1 {
		t.Errorf("snapshot should be isolated, got %d", snap.RequestCount)
	}

	// Mutating snapshot metrics should not affect original
	snap.PerEngine["pdf"].SuccessCount = 99
	if m.PerEngine["pdf"].SuccessCount != 1 {
		t.Errorf("snapshot engine metrics should be copies, got %d", m.PerEngine["pdf"].SuccessCount)
	}
}

func TestTracer_StartEndSpan(t *testing.T) {
	tr := NewTracer(1.0)

	span, ctx := tr.StartSpan(context.Background(), "gateway.pdf.convert", "trace-1", "")
	if span.TraceID != "trace-1" {
		t.Errorf("expected trace-1, got %s", span.TraceID)
	}
	if span.SpanID == "" {
		t.Error("expected generated span id")
	}
	if span.Status != "unset" {
		t.Errorf("expected unset status, got %s", span.Status)
	}

	// Span should be retrievable from context
	if GetSpanFromContext(ctx) != span {
		t.Error("expected span in context")
	}

	// Span should be recorded before ending
	if traces := tr.GetTrace("trace-1"); len(traces) != 1 {
		t.Errorf("expected 1 span, got %d", len(traces))
	}

	tr.EndSpan(span, "ok")
	if span.Status != "ok" {
		t.Errorf("expected ok status, got %s", span.Status)
	}
	if span.End.IsZero() {
		t.Error("expected end time set")
	}
}

func TestTracer_GeneratesTraceID(t *testing.T) {
	tr := NewTracer(1.0)

	span, _ := tr.StartSpan(context.Background(), "name", "", "")
	if span.TraceID == "" {
		t.Error("expected generated trace id")
	}
}

func TestTracer_EndSpanNil(t *testing.T) {
	tr := NewTracer(1.0)
	tr.EndSpan(nil, "ok") // must not panic
}

func TestTracer_MultipleSpansPerTrace(t *testing.T) {
	tr := NewTracer(1.0)

	span1, ctx := tr.StartSpan(context.Background(), "parent", "trace-1", "")
	span2, _ := tr.StartSpan(ctx, "child", "trace-1", span1.SpanID)

	if span2.ParentSpanID != span1.SpanID {
		t.Errorf("expected parent %s, got %s", span1.SpanID, span2.ParentSpanID)
	}

	if traces := tr.GetTrace("trace-1"); len(traces) != 2 {
		t.Errorf("expected 2 spans, got %d", len(traces))
	}
}
