package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg := Load()

	resolver := NewHTTPResolver(cfg.ControlPlaneURL)

	reporter, err := NewReporter(cfg.ControlPlaneURL, cfg.UsageReporterID, cfg.UsageReporterSecret)
	if err != nil {
		log.Fatalf("Failed to create usage reporter: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		reporter.Close(ctx)
	}()

	proxy, err := NewProxy(resolver, reporter)
	if err != nil {
		log.Fatalf("Failed to build proxy: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", proxy)

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
		// Cap header read time (slowloris); body and response are streaming
		// uploads/downloads — leave their timeouts at the load-balancer layer.
		ReadHeaderTimeout: 30 * time.Second,
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-sigChan
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
}
