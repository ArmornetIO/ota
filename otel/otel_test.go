package otel

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ArmornetIO/ota"
)

func TestSubscriberRecordsStagesDurationAndSpans(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	sub := Subscriber(mp, tp)
	t0 := time.Unix(1000, 0)
	ev := func(s ota.Stage, d time.Duration, err error) {
		sub(ota.Event{Stage: s, Program: "app", Version: "2.0.0", At: t0.Add(d), Err: err})
	}
	ev(ota.StageOfferSeen, 0, nil)
	ev(ota.StageDownloading, time.Second, nil)
	ev(ota.StageDownloading, 2*time.Second, nil) // progress repeat: ignored
	ev(ota.StageVerified, 4*time.Second, nil)
	ev(ota.StageInstalled, 5*time.Second, nil)
	// A second attempt that fails.
	ev(ota.StageOfferSeen, 10*time.Second, nil)
	ev(ota.StageRefused, 11*time.Second, errors.New("bad signature"))

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("%d spans, want 2", len(spans))
	}
	if len(spans[0].Events()) != 4 || spans[0].Status().Code == codes.Error {
		t.Fatalf("first span: %d events, status %v", len(spans[0].Events()), spans[0].Status())
	}
	if spans[1].Status().Code != codes.Error || spans[1].Status().Description != "bad signature" {
		t.Fatalf("failed attempt status = %+v", spans[1].Status())
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			got[m.Name] = true
			if m.Name == "ota.download.duration" {
				h := m.Data.(metricdata.Histogram[float64])
				if h.DataPoints[0].Count != 1 || h.DataPoints[0].Sum != 3 {
					t.Fatalf("download duration = %+v", h.DataPoints[0])
				}
			}
		}
	}
	if !got["ota.update.stages"] || !got["ota.download.duration"] {
		t.Fatalf("metrics = %v", got)
	}
}
