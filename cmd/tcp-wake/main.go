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

	// The health belief and the probe that is its only writer (T4). The probe
	// runs while a request is pending and the request path releases on the
	// first ready observation.
	health := proxy.NewHealth()
	prober := proxy.NewProber(cfg, health)
	defer prober.Close()

	// TODO(T5, T6, T7): replace this hold-only handler with the wake trigger,
	// wait-bound timer, and forwarder. Until then an accepted request is held
	// open until its client disconnects or the target becomes healthy, and no
	// response bytes are written (FR-2, FR-6).
	listener := proxy.New(cfg, func(p *proxy.Pending) error {
		prober.RequestStarted()
		defer prober.RequestDone()
		health.WaitHealthyOr(ctx, p.Discarded())
		return nil
	})

	if err := listener.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
