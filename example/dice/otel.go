// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

// Initialize a gRPC connection to be used by both the tracer and meter
// providers.
func initConn() (*grpc.ClientConn, error) {
	// It connects the OpenTelemetry Collector through local gRPC connection.
	// You may replace `localhost:4317` with your endpoint.
	conn, err := grpc.NewClient("9.46.79.170:4317",
		// Note the use of insecure transport here. TLS is recommended in production.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC connection to collector: %w", err)
	}

	return conn, err
}

// setupOTelSDK bootstraps the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func setupOTelSDK(ctx context.Context) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	conn, err := initConn()
	if err != nil {
		handleErr(err)
		return
	}

	// Set up propagator.
	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	// Set up trace provider.
	tracerProvider, err := newTraceProvider(ctx, conn)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Set up meter provider.
	meterProvider, err := newMeterProvider(ctx, conn)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	// Record runtime metrics
	meter := otel.Meter("runtime")
	err = recordRuntimeMetrics(meter)
	if err != nil {
		handleErr(err)
		return
	}

	// Set up logger provider.
	loggerProvider, err := newLoggerProvider()
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)

	return
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newTraceProvider(ctx context.Context, conn *grpc.ClientConn) (*sdktrace.TracerProvider, error) {
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, err
	}

	// Create resource with default attributes
	defaultResource, _ := resource.New(context.Background(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
	)

	// Create resource with custom attributes
	customResource, _ := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String("dice-service"),
			semconv.ServiceInstanceIDKey.String("localhost"),
			semconv.DeploymentEnvironmentKey.String("local-dev"),
		),
	)

	// Merge the default resource with the custom one
	finalResource, _ := resource.Merge(defaultResource, customResource)

	// Register the trace exporter with a TracerProvider, using a batch
	// span processor to aggregate spans before export.
	bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(finalResource),
		sdktrace.WithSpanProcessor(bsp),
	)

	return tracerProvider, nil
}

func newMeterProvider(ctx context.Context, conn *grpc.ClientConn) (*sdkmetric.MeterProvider, error) {
	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("dice-service"),
			semconv.ServiceInstanceIDKey.String("localhost"),
			semconv.DeploymentEnvironmentKey.String("local-dev"),
		)),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
	)
	return meterProvider, nil
}

func newLoggerProvider() (*log.LoggerProvider, error) {
	logExporter, err := stdoutlog.New()
	if err != nil {
		return nil, err
	}

	loggerProvider := log.NewLoggerProvider(
		log.WithProcessor(log.NewBatchProcessor(logExporter)),
	)
	return loggerProvider, nil
}

func recordRuntimeMetrics(meter metric.Meter) error {
	// Create metric instruments

	heapUsed, err := meter.Float64ObservableGauge("go_heap_used")
	if err != nil {
		return err
	}

	gcPause, err := meter.Float64ObservableGauge("go_gc_pause")
	if err != nil {
		return err
	}

	goroutines, err := meter.Int64ObservableGauge("go_goroutines")
	if err != nil {
		return err
	}

	allocatedMemory, err := meter.Float64ObservableGauge("go_allocated_memory")
	if err != nil {
		return err
	}

	memoryObtainedFromSystem, err := meter.Float64ObservableGauge("go_memory_obtained_from_system")
	if err != nil {
		return err
	}

	heapObjects, err := meter.Int64ObservableGauge("go_heap_objects")
	if err != nil {
		return err
	}

	systemHeap, err := meter.Float64ObservableGauge("go_system_heap")
	if err != nil {
		return err
	}

	// Record the runtime stats periodically
	if _, err := meter.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)

			o.ObserveFloat64(heapUsed, float64(memStats.HeapAlloc)/1024/1024)           // Heap Used in MB
			o.ObserveFloat64(gcPause, float64(memStats.PauseTotalNs)/1e6)               // GC Pause in ms
			o.ObserveInt64(goroutines, int64(runtime.NumGoroutine()))                   // Number of Goroutines
			o.ObserveFloat64(allocatedMemory, float64(memStats.Alloc)/1024/1024)        // Allocated Memory in MB
			o.ObserveFloat64(memoryObtainedFromSystem, float64(memStats.Sys)/1024/1024) // Memory Obtained From System in MB
			o.ObserveInt64(heapObjects, int64(memStats.HeapObjects))                    // Heap Objects
			o.ObserveFloat64(systemHeap, float64(memStats.HeapSys)/1024/1024)           // System Heap in MB
			return nil
		},
		heapUsed, gcPause, goroutines, allocatedMemory, memoryObtainedFromSystem, heapObjects, systemHeap,
	); err != nil {
		return err
	}

	return nil
}

