//go:build integration

package main

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestFxAppStartsAndStopsCleanly(t *testing.T) {
	requireEnv(t)

	goroutinesBefore := runtime.NumGoroutine()

	application := fxtest.New(t, modules(), fx.NopLogger)

	startCtx, cancelStart := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelStart()
	application.RequireStart()

	select {
	case <-startCtx.Done():
		t.Fatal("startup exceeded its budget")
	default:
	}

	assertServing(t)

	stopped := make(chan struct{})
	go func() {
		application.RequireStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(60 * time.Second):
		t.Fatal("shutdown did not finish within 60s")
	}

	assertNotServing(t)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= goroutinesBefore+5 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Errorf("goroutines leaked after shutdown: %d before, %d after",
		goroutinesBefore, runtime.NumGoroutine())
}

func requireEnv(t *testing.T) {
	t.Helper()

	if os.Getenv("JUNGLE_PORT") == "" {
		t.Setenv("JUNGLE_PORT", "18080")
	}
	if os.Getenv("SQS_ENDPOINT_URL") == "" {
		t.Setenv("SQS_ENDPOINT_URL", "http://localhost:4566")
	}
	if os.Getenv("SQS_REQUEST_QUEUE_URL") == "" {
		t.Setenv("SQS_REQUEST_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo")
	}
	if os.Getenv("SQS_EVENT_QUEUE_URL") == "" {
		t.Setenv("SQS_EVENT_QUEUE_URL", "http://localhost:4566/000000000000/wager-events.fifo")
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		t.Setenv("AWS_ACCESS_KEY_ID", "test")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	}
	if os.Getenv("OIDC_ISSUER_URL") == "" {
		t.Setenv("OIDC_ISSUER_URL", "http://localhost:8081/realms/jungle")
	}
}

func assertServing(t *testing.T) {
	t.Helper()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.Get("http://localhost:" + os.Getenv("JUNGLE_PORT") + "/health/live")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Fatal("the app never answered /health/live after RequireStart")
}

func assertNotServing(t *testing.T) {
	t.Helper()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:" + os.Getenv("JUNGLE_PORT") + "/health/live")
	if err == nil {
		resp.Body.Close()
		t.Error("the app is still serving after RequireStop; the listener was not released")
	}
}
