// Command mortise is a minimal, self-hosted OpenAI-compatible reverse proxy
// fronting vLLM fleets and external OpenAI-compatible endpoints.
//
// @title                       mortise
// @version                     0.1.0
// @description                 A minimal, self-hosted OpenAI-compatible AI gateway: model-name routing, retries/failover, per-key limits, token accounting, idempotent replay, and OpenTelemetry.
// @description                 Bodies are OpenAI-compatible and forwarded verbatim; mortise only inspects model, stream, and usage.
// @BasePath                    /
// @securityDefinitions.apikey  BearerAuth
// @in                          header
// @name                        Authorization
// @description                 Client API key presented as "Bearer <key>".
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

// Build information, injected via -ldflags at release time (see .goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	configPath := flag.String("config", "mortise.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("mortise %s (commit %s, built %s)\n", version, commit, date)
		return
	}

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

	log.Printf("mortise %s listening on %s", version, cfg.Listen)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("mortise: %v", err)
	}
	log.Printf("mortise stopped")
	_ = os.Stdout.Sync()
}
