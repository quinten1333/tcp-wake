// Command tcp-wake is a wake-on-demand reverse proxy that holds requests to a
// powered-off host and forwards them once it is healthy.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"tcp-wake/internal/config"
	"tcp-wake/internal/proxy"
)

func main() {
	cfg, err := config.Load(os.Args[1:], os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The health belief, the probe that writes it (T4), the wake trigger, and
	// the one-line log (T5). The pipeline wakes a not-healthy target once per
	// request and holds the request until the probe reports it healthy.
	health := proxy.NewHealth()
	prober := proxy.NewProber(cfg, health)
	defer prober.Close()
	logger := proxy.NewLogger(os.Stdout)
	wake := proxy.NewWakeTrigger(cfg)
	forward, err := proxy.NewForwarder(cfg.TargetAddress)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	pl := proxy.NewPipeline(ctx, cfg.WaitBound, health, prober, wake, forward, logger)
	listener := proxy.NewListener(cfg, pl.Handle)

	if err := listener.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
