package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

type Manager struct {
	tp      trace.TracerProvider
	tracer  trace.Tracer
	mp      metric.MeterProvider
	meter   metric.Meter
	enabled bool
	cleanFn func(context.Context) error

	// Instruments
	stepDuration   metric.Float64Histogram
	jobDuration    metric.Float64Histogram
	jobsCompleted  metric.Int64Counter
	stepsCompleted metric.Int64Counter
}

func (m *Manager) initInstruments() error {
	if m.meter == nil {
		return nil
	}
	var err error
	m.stepDuration, err = m.meter.Float64Histogram(
		"actr.step.duration_seconds",
		metric.WithDescription("Duration of runner step execution in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	m.jobDuration, err = m.meter.Float64Histogram(
		"actr.job.duration_seconds",
		metric.WithDescription("Duration of job execution in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return err
	}

	m.jobsCompleted, err = m.meter.Int64Counter(
		"actr.jobs.completed_total",
		metric.WithDescription("Total number of completed jobs"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return err
	}

	m.stepsCompleted, err = m.meter.Int64Counter(
		"actr.steps.completed_total",
		metric.WithDescription("Total number of completed steps"),
		metric.WithUnit("1"),
	)
	return err
}

func NewManager(ctx context.Context, serviceName string) (*Manager, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		// Zero-config default: local trace generator with no network export
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
		)
		mp := sdkmetric.NewMeterProvider()
		mgr := &Manager{
			tp:      tp,
			tracer:  tp.Tracer("actr"),
			mp:      mp,
			meter:   mp.Meter("actr"),
			enabled: false,
			cleanFn: func(ctx context.Context) error {
				var errs []error
				if err := tp.Shutdown(ctx); err != nil {
					errs = append(errs, err)
				}
				if err := mp.Shutdown(ctx); err != nil {
					errs = append(errs, err)
				}
				return errors.Join(errs...)
			},
		}
		_ = mgr.initInstruments()
		return mgr, nil
	}

	protocol := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	var traceExporter sdktrace.SpanExporter
	var metricExporter sdkmetric.Exporter
	var err error

	if protocol == "http/protobuf" || protocol == "http" {
		traceExporter, err = otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		metricExporter, err = otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		traceExporter, err = otlptracegrpc.New(ctx)
		if err != nil {
			return nil, err
		}
		metricExporter, err = otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, err
		}
	}

	res, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	mgr := &Manager{
		tp:      tp,
		tracer:  tp.Tracer("actr"),
		mp:      mp,
		meter:   mp.Meter("actr"),
		enabled: true,
		cleanFn: func(ctx context.Context) error {
			var errs []error
			if err := tp.Shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
			if err := mp.Shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
			return errors.Join(errs...)
		},
	}
	if err := mgr.initInstruments(); err != nil {
		_ = mgr.Shutdown(ctx)
		return nil, err
	}

	return mgr, nil
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

// InjectStepTraceContext injects standard W3C Trace Context (TRACEPARENT and TRACESTATE)
// into the environment map for child build tools, compilers, and test runners.
func (m *Manager) InjectStepTraceContext(ctx context.Context, env map[string]string) {
	if env == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	if span == nil {
		return
	}
	sc := span.SpanContext()
	if !sc.IsValid() {
		return
	}
	flags := sc.TraceFlags().String()
	if flags == "" {
		flags = "01"
	}
	env["TRACEPARENT"] = fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), flags)
	if state := sc.TraceState().String(); state != "" {
		env["TRACESTATE"] = state
	}
}

// RecordSpanError records an error on the span and marks its status as Error.
func (m *Manager) RecordSpanError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// FinishStepSpan sets the step exit code and terminal status before completing the span.
func (m *Manager) FinishStepSpan(span trace.Span, exitCode int, status string) {
	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.Int("actr.step_exit_code", exitCode),
		attribute.String("actr.step_status", status),
	)
	if exitCode != 0 && status != "success" && status != "skipped" {
		span.SetStatus(codes.Error, fmt.Sprintf("step exited with code %d", exitCode))
	} else {
		span.SetStatus(codes.Ok, "")
	}
}

// RecordJobCompletion records job duration and completion counter with status and metadata attributes.
func (m *Manager) RecordJobCompletion(ctx context.Context, jobID, workflow, repo, owner, status string, durationSec float64) {
	if m == nil || m.meter == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("actr.job_id", jobID),
		attribute.String("actr.workflow", workflow),
		attribute.String("actr.repo", repo),
		attribute.String("actr.status", status),
	}
	if owner != "" {
		attrs = append(attrs, attribute.String("actr.owner", owner))
	}

	if m.jobDuration != nil {
		m.jobDuration.Record(ctx, durationSec, metric.WithAttributes(attrs...))
	}
	if m.jobsCompleted != nil {
		m.jobsCompleted.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordStepCompletion records step duration and completion counter with status and exit code.
func (m *Manager) RecordStepCompletion(ctx context.Context, stepID, stepName, status string, exitCode int, durationSec float64) {
	if m == nil || m.meter == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("actr.step_id", stepID),
		attribute.String("actr.step_name", stepName),
		attribute.String("actr.status", status),
		attribute.Int("actr.step_exit_code", exitCode),
	}

	if m.stepDuration != nil {
		m.stepDuration.Record(ctx, durationSec, metric.WithAttributes(attrs...))
	}
	if m.stepsCompleted != nil {
		m.stepsCompleted.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}
