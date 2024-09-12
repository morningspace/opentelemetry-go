// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build go1.22
// +build go1.22

package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const name = "go.opentelemetry.io/otel/example/dice"

var (
	tracer  = otel.Tracer(name)
	meter   = otel.Meter(name)
	logger  = otelslog.NewLogger(name)
	rollCnt metric.Int64Counter
)

func init() {
	var err error
	rollCnt, err = meter.Int64Counter("dice.rolls",
		metric.WithDescription("The number of rolls by roll value"),
		metric.WithUnit("{roll}"))
	if err != nil {
		panic(err)
	}
}

func rolldice(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "roll")
	defer span.End()

	// Simulate a nested call to a remote service or database
	callRemoteService(ctx)

	roll := 1 + rand.Intn(6)

	var msg string
	if player := r.PathValue("player"); player != "" {
		msg = fmt.Sprintf("%s is rolling the dice", player)
	} else {
		msg = "Anonymous player is rolling the dice"
	}
	logger.InfoContext(ctx, msg, "result", roll)

	rollValueAttr := attribute.Int("roll.value", roll)
	span.SetAttributes(rollValueAttr)
	rollCnt.Add(ctx, 1, metric.WithAttributes(rollValueAttr))

	resp := strconv.Itoa(roll) + "\n"
	if _, err := io.WriteString(w, resp); err != nil {
		logger.ErrorContext(ctx, "Write failed", "error", err)
	}
}

func callRemoteService(ctx context.Context) {
	// Start a child span to represent the external call (EXIT span)
	_, span := tracer.Start(ctx, "callRemoteService", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// Simulate latency for the external call
	time.Sleep(100 * time.Millisecond)

	// Simulate a successful external call
	span.SetAttributes(
		attribute.String("peer.service", "database"),
		attribute.String("http.request.method", "GET"),
		attribute.String("http.route", "/categories"),
		attribute.String("url.path", "/categories"),
		attribute.Bool("success", true),
	)
	fmt.Println("Remote service call completed")
}
