// Package otel turns ota's update events into OpenTelemetry metrics and
// traces. It is a separate module so the updater core carries no
// OpenTelemetry dependency; programs that want it add one line:
//
//	opts.OnEvent = otaotel.Subscriber(meterProvider, tracerProvider)
package otel

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/ArmornetIO/ota"
)

const scope = "github.com/ArmornetIO/ota"

// terminal stages end an attempt within this process. installed ends it too:
// after the swap the process restarts, and the new one reports the verdict.
var terminal = map[ota.Stage]bool{
	ota.StageCommitted: true, ota.StageRolledBack: true, ota.StageFailed: true,
	ota.StageRefused: true, ota.StageDeferred: true, ota.StageStaged: true, ota.StageInstalled: true,
}

// Subscriber returns an ota event handler that records:
//
//   - ota.update.stages: a counter per stage reached (attribute ota.stage);
//   - ota.download.duration: seconds from the first download byte to verified;
//   - one span per attempt ("ota.update"), with an event per stage, ended as an
//     error when the attempt failed, was refused or rolled back.
func Subscriber(mp metric.MeterProvider, tp trace.TracerProvider) func(ota.Event) {
	m := mp.Meter(scope)
	stages, _ := m.Int64Counter("ota.update.stages", metric.WithDescription("Update stages reached"))
	dl, _ := m.Float64Histogram("ota.download.duration", metric.WithUnit("s"), metric.WithDescription("Artifact download and verification time"))
	tracer := tp.Tracer(scope)

	var (
		mu        sync.Mutex
		span      trace.Span
		lastStage ota.Stage
		dlStart   time.Time
	)
	return func(e ota.Event) {
		mu.Lock()
		defer mu.Unlock()
		// Download progress arrives many times per second; only transitions count.
		if e.Stage == lastStage {
			return
		}
		lastStage = e.Stage
		ctx := context.Background()
		attrs := []attribute.KeyValue{attribute.String("ota.program", e.Program), attribute.String("ota.version", e.Version)}
		stages.Add(ctx, 1, metric.WithAttributes(append(attrs, attribute.String("ota.stage", string(e.Stage)))...))

		switch e.Stage {
		case ota.StageDownloading:
			dlStart = e.At
		case ota.StageVerified:
			if !dlStart.IsZero() {
				dl.Record(ctx, e.At.Sub(dlStart).Seconds(), metric.WithAttributes(attrs...))
				dlStart = time.Time{}
			}
		}

		if span == nil {
			_, span = tracer.Start(ctx, "ota.update", trace.WithTimestamp(e.At), trace.WithAttributes(attrs...))
		}
		evAttrs := []attribute.KeyValue{attribute.String("ota.stage", string(e.Stage))}
		if e.Err != nil {
			evAttrs = append(evAttrs, attribute.String("error", e.Err.Error()))
		}
		span.AddEvent(string(e.Stage), trace.WithTimestamp(e.At), trace.WithAttributes(evAttrs...))
		if terminal[e.Stage] {
			switch e.Stage {
			case ota.StageFailed, ota.StageRefused, ota.StageRolledBack:
				msg := string(e.Stage)
				if e.Err != nil {
					msg = e.Err.Error()
				}
				span.SetStatus(codes.Error, msg)
			}
			span.End(trace.WithTimestamp(e.At))
			span, lastStage = nil, ""
		}
	}
}
