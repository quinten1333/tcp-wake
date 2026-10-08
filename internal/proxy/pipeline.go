package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Pipeline is the request path in the order RS-2 specifies: a request that
// arrives while the target is not healthy wakes it, then every request holds
// until the target is healthy, the wait bound elapses, or the client goes away,
// and a request that becomes healthy is forwarded to the target.
type Pipeline struct {
	ctx context.Context
	// waitBound is how long a request may be held, measured from its arrival
	// and never restarted (FR-8). It is a value rather than the whole config so
	// the pipeline depends only on what it uses.
	waitBound time.Duration
	health    *Health
	prober    *Prober
	wake      *WakeTrigger
	forward   *Forwarder
	logger    *Logger
}

// NewPipeline wires the request path. ctx is the process context: it cancels an
// in-flight wake command and bounds the wait for health on shutdown. waitBound
// is the configured hold limit (FR-8).
func NewPipeline(ctx context.Context, waitBound time.Duration, health *Health, prober *Prober, wake *WakeTrigger, forward *Forwarder, logger *Logger) *Pipeline {
	return &Pipeline{
		ctx:       ctx,
		waitBound: waitBound,
		health:    health,
		prober:    prober,
		wake:      wake,
		forward:   forward,
		logger:    logger,
	}
}

// Handle runs one retained request. It assumes the target is up: it first tries
// to connect and forward, with the connect bounded by the configured probe
// timeout (ADR-0018). Only when the connection cannot be established is the
// target treated as off — its state becomes unhealthy, the request is held, and
// the wake command runs once for this request (FR-3). A failed wake gives that
// client an immediate 500 naming the wake command (FR-17). A failure after the
// connection was established is the FR-18 502.
func (pl *Pipeline) Handle(p *Pending) {
	if err := pl.forward.Forward(p); err == nil {
		// The target was reachable and answered; record the positive
		// observation so a stale not-healthy belief is corrected and logged.
		pl.prober.Observe(true)
		return
	} else if !errors.Is(err, ErrTargetUnreachable) {
		// The connection was established but the exchange failed. A probe
		// repairs the belief rather than inferring it from the failure's cause
		// (FR-18, FR-19, ADR-0008).
		pl.writeAndLog(p.Conn, http.StatusBadGateway, errorDetail{
			Message:   fmt.Sprintf("forward to target %s failed: %v", pl.forward.Address(), err),
			Component: componentTarget,
		})
		pl.prober.ProbeNow(pl.ctx)
		return
	}

	// The target is off: mark it unhealthy, wake it, and hold.
	pl.prober.Observe(false)
	res := pl.wake.Run(pl.ctx)
	pl.logger.Wake(pl.wake.Command(), res)
	if res.Err != nil {
		// A wake interrupted by shutdown is not a wake failure: the process is
		// going away, and FR-16/§6.8 say a held client gets no response.
		if pl.ctx.Err() != nil {
			return
		}
		pl.writeAndLog(p.Conn, http.StatusInternalServerError, errorDetail{
			Message:   fmt.Sprintf("wake command %q failed: %v", pl.wake.Command(), res.Err),
			Component: componentWakeCommand,
		})
		return
	}

	pl.prober.RequestStarted()
	defer pl.prober.RequestDone()

	// The deadline is anchored to the request's arrival and computed once, so the
	// failed connect attempt cannot restart or extend it (FR-8). It derives from
	// the process context, so a healthy observation wins even at the boundary and
	// a shutdown cancels the wait.
	waitCtx, cancel := context.WithDeadline(pl.ctx, p.Arrival.Add(pl.waitBound))
	defer cancel()

	if pl.health.WaitHealthyOr(waitCtx, p.Discarded()) {
		// The target is healthy: forward on this request's own upstream
		// connection (FR-4). A failure now is the post-wake transport failure
		// (FR-18), and the probe repairs the belief (FR-19).
		if err := pl.forward.Forward(p); err != nil {
			pl.writeAndLog(p.Conn, http.StatusBadGateway, errorDetail{
				Message:   fmt.Sprintf("forward to target %s failed: %v", pl.forward.Address(), err),
				Component: componentTarget,
			})
			pl.prober.ProbeNow(pl.ctx)
		}
		return
	}

	// Three ways to leave without becoming healthy. A client that left gets no
	// response (FR-7), and a shutdown is silent (FR-16, §6.8); only the wait
	// bound itself produces the 504.
	if pl.ctx.Err() != nil || isClosed(p.Discarded()) {
		return
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		pl.writeAndLog(p.Conn, http.StatusGatewayTimeout, errorDetail{
			Message:   fmt.Sprintf("target did not become healthy within the wait bound of %s", pl.waitBound),
			Component: componentWaitBound,
			Limit:     pl.waitBound.String(),
		})
	}
}

// writeAndLog writes one error response and logs the same detail, so the body
// the client receives and the line the log records cannot diverge (ADR-0009,
// ADR-0010). It logs even when the write fails, because the condition occurred
// whether or not the client was still there to hear it.
func (pl *Pipeline) writeAndLog(conn net.Conn, status int, detail errorDetail) {
	writeError(conn, status, detail)
	pl.logger.Error(status, detail)
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
