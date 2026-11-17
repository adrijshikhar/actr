package telemetry

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

type Manager struct {
	tp      trace.TracerProvider
	tracer  trace.Tracer
	enabled bool
	cleanFn func(context.Context) error
}

func NewManager(ctx context.Context, serviceName string) (*Manager, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		// Zero-config default: local trace generator with no network export
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
		)
		return &Manager{
			tp:      tp,
			tracer:  tp.Tracer("actr"),
			enabled: false,
			cleanFn: tp.Shutdown,
		}, nil
	}

	protocol := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	var exporter sdktrace.SpanExporter
	var err error

	if protocol == "http/protobuf" || protocol == "http" {
		exporter, err = otlptracehttp.New(ctx)
	} else {
		exporter, err = otlptracegrpc.New(ctx)
	}
	if err != nil {
		return nil, err
	}

	res, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return &Manager{
		tp:      tp,
		tracer:  tp.Tracer("actr"),
		enabled: true,
		cleanFn: tp.Shutdown,
	}, nil
}

func (m *Manager) Enabled() bool {
	if m == nil {
		return false
	}
	return m.enabled
}

func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil || m.cleanFn == nil {
		return nil
	}
	return m.cleanFn(ctx)
}

func (m *Manager) StartJobSpan(ctx context.Context, jobID, workflow, repo, owner string) (context.Context, trace.Span) {
	if m == nil || m.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return m.tracer.Start(ctx, "actr.job:"+jobID,
		trace.WithAttributes(
			attribute.String("actr.job_id", jobID),
			attribute.String("actr.workflow", workflow),
			attribute.String("actr.repo", repo),
			attribute.String("actr.owner", owner),
		),
	)
}

func (m *Manager) StartStepSpan(ctx context.Context, stepID, stepName string) (context.Context, trace.Span) {
	if m == nil || m.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return m.tracer.Start(ctx, "actr.step:"+stepName,
		trace.WithAttributes(
			attribute.String("actr.step_id", stepID),
			attribute.String("actr.step_name", stepName),
		),
	)
}
