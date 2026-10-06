package proxy

import (
	"bufio"
	"errors"
	"net"
	"net/http"

	"tcp-wake/internal/config"
)

// ErrFraming means the bytes on the connection are not a complete HTTP
// request. The connection is closed without a synthetic response, because
// ADR-0009 makes the four JSON error bodies the only responses the system
// produces; an unparseable request is treated as not accepted.
var ErrFraming = errors.New("proxy: malformed request framing")

// ErrBodyTooLarge means the request body exceeded held_body_cap (FR-20). Intake
// has already written the 413 naming the cap and closed the connection.
var ErrBodyTooLarge = errors.New("proxy: request body exceeds the held body cap")

// maxHeadBytes bounds the header block read before a request is declared
// malformed. It is an internal safety guard, not a specification limit: the
// spec caps only the body (FR-20), and a body that never completes is held
// indefinitely by design (R-4).
const maxHeadBytes = 1 << 20

// Intake reads exactly one HTTP request from conn, retaining the bytes verbatim
// in Pending.Bytes, and detects its completion by framing without interpreting
// it (ADR-0004). It reads exactly as far as the framing requires, so no byte of
// a following request can be misattributed to this one.
func Intake(conn net.Conn, r *bufio.Reader, limit config.ByteSize) (*Pending, error) {
	head, err := readHead(r)
	if err != nil {
		return nil, err
	}

	contentLength, chunked, err := framing(head)
	if err != nil {
		return nil, err
	}

	raw := head
	if chunked {
		if err := readChunkedBody(r, &raw, limit); err != nil {
			if errors.Is(err, ErrBodyTooLarge) {
				rejectTooLarge(conn, limit)
			}
			return nil, err
		}
	} else if contentLength > 0 {
		if contentLength > int64(limit) {
			rejectTooLarge(conn, limit)
			return nil, ErrBodyTooLarge
		}
		if err := appendExact(r, &raw, contentLength); err != nil {
			return nil, err
		}
	}

	return newPending(conn, r, raw), nil
}

// rejectTooLarge writes the 413 naming the cap (FR-20, ADR-0014) and closes the
// connection. A rejection happens before the request is held, so closing is
// safe and the client is not left waiting.
func rejectTooLarge(conn net.Conn, limit config.ByteSize) {
	writeError(conn, http.StatusRequestEntityTooLarge, heldBodyCapDetail(limit))
	conn.Close()
}
