package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"tcp-wake/internal/config"
)

// The four component tokens of the ADR-0014 error schema. They are an
// enumerable set, so an agent can branch on them without parsing prose
// (ADR-0009). Keep them as constants so the code and its tests cannot drift.
const (
	componentWaitBound   = "wait_bound"
	componentWakeCommand = "wake_command"
	componentTarget      = "target"
	componentHeldBodyCap = "held_body_cap"
)

// errorEnvelope is the response-body schema from ADR-0014: a nested error
// object carrying message, an enumerable component, and, for the two bounded
// conditions, a limit. It mirrors hypha's own {"error":{"message":…}} envelope
// so one client parser handles both the origin's errors and the proxy's.
type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message   string `json:"message"`
	Component string `json:"component"`
	Limit     string `json:"limit,omitempty"`
}

// heldBodyCapDetail builds the 413 detail once, so the response the client
// receives and the line the logger writes cannot drift. The Listener reuses it
// to log a body-cap rejection, since that error is produced by Intake rather
// than by the pipeline.
func heldBodyCapDetail(limit config.ByteSize) errorDetail {
	return errorDetail{
		Message:   fmt.Sprintf("request body exceeds the held body cap of %s", limit),
		Component: componentHeldBodyCap,
		Limit:     limit.String(),
	}
}

// writeError writes one complete HTTP response to conn and leaves the
// connection open for the caller to close.
//
// It is the only place this system writes a response of its own: the four
// error paths are the four bodies of ADR-0009, and everything else a client
// receives is hypha's response relayed verbatim. That single seam is what makes
// NFR-6 ("exactly one response per accepted request") inspectable. The four
// paths are the wait bound (504), the wake command (500), a transport-level
// forward failure (502), and the held body cap (413).
//
// The caller passes the detail once and may also hand the same value to
// Logger.Error, so the log records exactly what the client was told.
//
// detail.Limit carries the bounded condition's value as a string: for the wait
// bound it is the Go canonical duration form ("2m0s" for a 120s configuration)
// and for the body cap it is ByteSize's IEC form ("64MiB"). Architecture §5.1
// illustrates the former as "120s"; the value names the configured bound, which
// is what ADR-0014 requires, and the original TOML spelling is not retained.
//
// There is no error return: marshalling a struct of strings cannot fail, and a
// failed write means the client has already left, which every caller treats the
// same way.
func writeError(conn net.Conn, status int, detail errorDetail) {
	body, _ := json.Marshal(errorEnvelope{Error: detail})
	head := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\n"+
			"Content-Type: application/json\r\n"+
			"Content-Length: %d\r\n"+
			"Connection: close\r\n"+
			"\r\n",
		status, http.StatusText(status), len(body),
	)
	io.WriteString(conn, head)
	conn.Write(body)
}
