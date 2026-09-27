package proxy

import (
	"context"
	"fmt"
	"net/http"
)

// Pipeline is the request path in the order RS-2 specifies: a request that
// arrives while the target is not healthy wakes it, and every request then
// holds until the target is healthy or the client goes away. T6 adds the
// wait-bound timer and T7 the forwarder to Handle; they extend this type rather
// than replace it.
type Pipeline struct {
	ctx    context.Context
	health *Health
	prober *Prober
	wake   *WakeTrigger
	logger *Logger
}

// NewPipeline wires the request path. ctx is the process context: it cancels an
// in-flight wake command and bounds the wait for health on shutdown. The
// wait-bound timer and forwarder join here in T6 and T7.
func NewPipeline(ctx context.Context, health *Health, prober *Prober, wake *WakeTrigger, logger *Logger) *Pipeline {
	return &Pipeline{
		ctx:    ctx,
		health: health,
		prober: prober,
		wake:   wake,
		logger: logger,
	}
}

// Handle runs one retained request. A request received while the target is not
// healthy triggers its own wake-command execution (FR-3); a failed execution
// gives that request's client an immediate 500 naming the wake command (FR-17)
// and never reaches the probe or the hold. Otherwise the probe starts and the
// request holds.
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
	pl.health.WaitHealthyOr(pl.ctx, p.Discarded())
}
