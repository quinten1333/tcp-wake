package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Pipeline is the request path in the order RS-2 specifies: a request that
// arrives while the target is not healthy wakes it, and every request then
// holds until the target is healthy, the wait bound elapses, or the client goes
// away. T7 adds the forwarder to Handle; it extends this type rather than
// replacing it.
type Pipeline struct {
	ctx context.Context
	// waitBound is how long a request may be held, measured from its arrival
	// and never restarted (FR-8). It is a value rather than the whole config so
	// the pipeline depends only on what it uses.
	waitBound time.Duration
	health    *Health
	prober    *Prober
	wake      *WakeTrigger
	logger    *Logger
}

// NewPipeline wires the request path. ctx is the process context: it cancels an
// in-flight wake command and bounds the wait for health on shutdown. waitBound
// is the configured hold limit (FR-8); the forwarder joins in T7.
func NewPipeline(ctx context.Context, waitBound time.Duration, health *Health, prober *Prober, wake *WakeTrigger, logger *Logger) *Pipeline {
	return &Pipeline{
		ctx:       ctx,
		waitBound: waitBound,
		health:    health,
		prober:    prober,
		wake:      wake,
		logger:    logger,
	}
}

// Handle runs one retained request. A request received while the target is not
// healthy triggers its own wake-command execution (FR-3); a failed execution
// gives that request's client an immediate 500 naming the wake command (FR-17)
// and never reaches the probe or the hold. Otherwise the probe starts and the
// request holds under the wait bound.
func (pl *Pipeline) Handle(p *Pending) {
	if !pl.health.Healthy() {
		res := pl.wake.Run(pl.ctx)
		pl.logger.Wake(pl.wake.Command(), res)
		if res.Err != nil {
			writeError(p.Conn, http.StatusInternalServerError, "wake_command",
				fmt.Sprintf("wake command %q failed: %v", pl.wake.Command(), res.Err), "")
			return
		}
	}

	pl.prober.RequestStarted()
	defer pl.prober.RequestDone()

	// The deadline is anchored to the request's arrival and computed once, so a
	// wake attempt cannot restart or extend it (FR-8). It derives from the
	// process context, so a healthy observation wins even at the boundary and
	// a shutdown cancels the wait.
	waitCtx, cancel := context.WithDeadline(pl.ctx, p.Arrival.Add(pl.waitBound))
	defer cancel()

	if pl.health.WaitHealthyOr(waitCtx, p.Discarded()) {
		return
	}

	// Three ways to leave without becoming healthy. A client that left gets no
	// response (FR-7), and a shutdown is silent (FR-16, §6.8); only the wait
	// bound itself produces the 504.
	if pl.ctx.Err() != nil || isClosed(p.Discarded()) {
		return
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		writeError(p.Conn, http.StatusGatewayTimeout, "wait_bound",
			fmt.Sprintf("target did not become healthy within the wait bound of %s", pl.waitBound),
			pl.waitBound.String())
	}
}

// isClosed reports whether ch has been closed, without blocking.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
