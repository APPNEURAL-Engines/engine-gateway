package telemetry

import (
	"context"
	"log"
	"sync"
	"time"
)

// Logger provides structured logging for the gateway.
type Logger interface {
	// Debug logs a debug message.
	Debug(msg string, fields map[string]interface{})

	// Info logs an info message.
	Info(msg string, fields map[string]interface{})

	// Warn logs a warning message.
	Warn(msg string, fields map[string]interface{})

	// Error logs an error message.
	Error(msg string, err error, fields map[string]interface{})
}

// LogLevel represents logging levels.
type LogLevel int

const (
	// LevelDebug logs everything.
	LevelDebug LogLevel = iota
	// LevelInfo logs informational messages.
	LevelInfo
	// LevelWarn logs warnings.
	LevelWarn
	// LevelError logs errors only.
	LevelError
)

// StdLogger implements Logger using the standard library logger.
type StdLogger struct {
	mu    sync.Mutex
	level LogLevel
}

// NewStdLogger creates a standard library logger.
func NewStdLogger(level LogLevel) *StdLogger {
	return &StdLogger{level: level}
}

// Debug logs a debug message.
func (l *StdLogger) Debug(msg string, fields map[string]interface{}) {
	l.log(LevelDebug, "DEBUG", msg, nil, fields)
}

// Info logs an info message.
func (l *StdLogger) Info(msg string, fields map[string]interface{}) {
	l.log(LevelInfo, "INFO", msg, nil, fields)
}

// Warn logs a warning message.
func (l *StdLogger) Warn(msg string, fields map[string]interface{}) {
	l.log(LevelWarn, "WARN", msg, nil, fields)
}

// Error logs an error message.
func (l *StdLogger) Error(msg string, err error, fields map[string]interface{}) {
	l.log(LevelError, "ERROR", msg, err, fields)
}

func (l *StdLogger) log(level LogLevel, label string, msg string, err error, fields map[string]interface{}) {
	if level < l.level {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().Format(time.RFC3339)
	line := ts + " [" + label + "] " + msg

	if err != nil {
		line += " error=" + err.Error()
	}

	if fields != nil {
		for k, v := range fields {
			line += " " + k + "=" + formatValue(v)
		}
	}

	log.Println(line)
}

// formatValue formats a field value.
func formatValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return itoa(int64(val))
	case int64:
		return itoa(val)
	case float64:
		return ftoa(val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case time.Duration:
		return val.String()
	default:
		return sprint(val)
	}
}

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

func ftoa(v float64) string {
	// Minimal float formatting
	if v == float64(int64(v)) {
		return itoa(int64(v))
	}
	return sprint(v)
}

func sprint(v interface{}) string {
	return "" + stringify(v)
}

// stringify is a minimal fmt replacement.
func stringify(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return itoa(int64(val))
	case int64:
		return itoa(val)
	case float64:
		return ftoa(val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return "<value>"
	}
}

// Metrics records gateway metrics.
type Metrics struct {
	mu sync.RWMutex

	// RequestCount total requests received.
	RequestCount int64

	// SuccessCount successful requests.
	SuccessCount int64

	// ErrorCount failed requests.
	ErrorCount int64

	// TimeoutCount timed out requests.
	TimeoutCount int64

	// TotalLatency accumulates request latency.
	TotalLatency time.Duration

	// PerEngine tracks per-engine metrics.
	PerEngine map[string]*EngineMetrics
}

// EngineMetrics tracks metrics for a specific engine.
type EngineMetrics struct {
	RequestCount int64
	SuccessCount int64
	ErrorCount   int64
	TotalLatency time.Duration
}

// NewMetrics creates a metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		PerEngine: make(map[string]*EngineMetrics),
	}
}

// RecordRequest records a request outcome.
func (m *Metrics) RecordRequest(engine string, success bool, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.RequestCount++
	m.TotalLatency += latency

	if success {
		m.SuccessCount++
	} else {
		m.ErrorCount++
	}

	em := m.PerEngine[engine]
	if em == nil {
		em = &EngineMetrics{}
		m.PerEngine[engine] = em
	}

	em.RequestCount++
	em.TotalLatency += latency
	if success {
		em.SuccessCount++
	} else {
		em.ErrorCount++
	}
}

// RecordTimeout records a timeout.
func (m *Metrics) RecordTimeout() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TimeoutCount++
}

// Snapshot returns a copy of current metrics.
func (m *Metrics) Snapshot() *Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := &Metrics{
		RequestCount: m.RequestCount,
		SuccessCount: m.SuccessCount,
		ErrorCount:   m.ErrorCount,
		TimeoutCount: m.TimeoutCount,
		TotalLatency: m.TotalLatency,
		PerEngine:    make(map[string]*EngineMetrics),
	}

	for k, v := range m.PerEngine {
		copied := *v
		snapshot.PerEngine[k] = &copied
	}

	return snapshot
}

// Tracer provides distributed tracing for the gateway.
type Tracer struct {
	mu     sync.RWMutex
	spans  map[string][]*Span
	sample float64
}

// Span represents a trace span.
type Span struct {
	// TraceID is the distributed trace identifier.
	TraceID string

	// SpanID is this span's identifier.
	SpanID string

	// ParentSpanID is the parent span.
	ParentSpanID string

	// Name is the span name.
	Name string

	// Start is when the span started.
	Start time.Time

	// End is when the span ended.
	End time.Time

	// Attributes are span attributes.
	Attributes map[string]string

	// Status is the span status.
	Status string
}

// NewTracer creates a tracer.
func NewTracer(sampleRate float64) *Tracer {
	return &Tracer{
		spans:  make(map[string][]*Span),
		sample: sampleRate,
	}
}

// StartSpan begins a new span.
func (t *Tracer) StartSpan(ctx context.Context, name string, traceID string, parentSpanID string) (*Span, context.Context) {
	if traceID == "" {
		traceID = generateID()
	}

	span := &Span{
		TraceID:      traceID,
		SpanID:       generateID(),
		ParentSpanID: parentSpanID,
		Name:         name,
		Start:        time.Now(),
		Attributes:   make(map[string]string),
		Status:       "unset",
	}

	t.mu.Lock()
	t.spans[traceID] = append(t.spans[traceID], span)
	t.mu.Unlock()

	return span, withSpan(ctx, span)
}

// EndSpan completes a span.
func (t *Tracer) EndSpan(span *Span, status string) {
	if span == nil {
		return
	}
	span.End = time.Now()
	if status != "" {
		span.Status = status
	}
}

// GetTrace returns all spans for a trace ID.
func (t *Tracer) GetTrace(traceID string) []*Span {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.spans[traceID]
}

// generateID creates a simple unique ID.
var idCounter uint64 = 0

func generateID() string {
	idCounter++
	return "span-" + itoa(int64(idCounter)) + "-" + itoa(time.Now().UnixNano()/1000000)
}

// contextKey for span storage.
type contextKey string

const spanContextKey contextKey = "gateway.span"

func withSpan(ctx context.Context, span *Span) context.Context {
	return context.WithValue(ctx, spanContextKey, span)
}

// GetSpanFromContext retrieves the current span.
func GetSpanFromContext(ctx context.Context) *Span {
	span, _ := ctx.Value(spanContextKey).(*Span)
	return span
}
