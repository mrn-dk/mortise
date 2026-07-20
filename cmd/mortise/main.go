// Command mortise is a minimal, self-hosted OpenAI-compatible reverse proxy
// fronting vLLM fleets and external OpenAI-compatible endpoints.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mrn-dk/mortise/internal/config"
	"github.com/mrn-dk/mortise/internal/server"
	"github.com/mrn-dk/mortise/internal/telemetry"
)

func main() {
	configPath := flag.String("config", "mortise.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("mortise: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tel, err := telemetry.Init(ctx, cfg.Telemetry)
	if err != nil {
		log.Fatalf("mortise: telemetry: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tel.Shutdown(shutdownCtx)
	}()

	srv := server.New(cfg, tel)
	go srv.Dedupe().Run(ctx)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Printf("mortise listening on %s", cfg.Listen)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("mortise: %v", err)
	}
	log.Printf("mortise stopped")
	_ = os.Stdout.Sync()
}
