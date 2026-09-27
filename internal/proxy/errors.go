package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

// writeError writes one complete HTTP response to conn and leaves the
// connection open for the caller to close. It is the seam T8 reuses for the
// other three error paths; T3 uses it for the held-body-cap 413.
func writeError(conn net.Conn, status int, component, message, limit string) error {
	body, err := json.Marshal(errorEnvelope{Error: errorDetail{
		Message:   message,
		Component: component,
		Limit:     limit,
	}})
	if err != nil {
		return err
	}
	head := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\n"+
			"Content-Type: application/json\r\n"+
			"Content-Length: %d\r\n"+
			"Connection: close\r\n"+
			"\r\n",
		status, http.StatusText(status), len(body),
	)
	if _, err := io.WriteString(conn, head); err != nil {
		return err
	}
	_, err = conn.Write(body)
	return err
}
