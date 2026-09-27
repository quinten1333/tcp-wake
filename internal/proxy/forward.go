package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
)

// Forwarder sends a held request's exact bytes to the configured target over
// plain HTTP and relays the response back unchanged and streamed (IF-2, FR-13,
// FR-14, FR-15). It is a transparent TCP relay: the retained wire bytes are
// written as they are, and the response is relayed with its framing tracked
// only to know where it ends, so nothing is re-serialised and byte-exactness is
// a property of the copy (ADR-0004). It applies no deadline of its own: the
// spec puts connection timeouts on the routing layer and the origin, not on
// this system (§8.5, §2 non-goal 7).
type Forwarder struct {
	target *url.URL
	host   string
}

// NewForwarder parses target_address (for example "http://hypha.lan:8080") and
// returns a Forwarder that dials its host. An unparseable address or one with
// no host is a start-time failure, not a per-request 502.
func NewForwarder(targetAddress string) (*Forwarder, error) {
	u, err := url.Parse(targetAddress)
	if err != nil {
		return nil, fmt.Errorf("proxy: invalid target_address %q: %w", targetAddress, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("proxy: target_address %q has no host", targetAddress)
	}
	return &Forwarder{target: u, host: u.Host}, nil
}

// Address returns the configured target, for the error body that names it
// (FR-18) and for the log.
func (f *Forwarder) Address() string {
	return f.target.String()
}

// Forward opens one upstream connection for this request, writes the retained
// bytes verbatim, and relays the response. Every held request calls this on its
// own, so none waits for another (FR-5). It returns an error only if it fails
// before writing any byte to the client, which is the one case where the caller
// can still produce the FR-18 502. Once the response head is on the wire the
// request already has its single response (NFR-6), so later failures just close
// the connection.
func (f *Forwarder) Forward(p *Pending) error {
	upstream, err := net.Dial("tcp", f.host)
	if err != nil {
		return fmt.Errorf("forward: dial %s: %w", f.host, err)
	}
	defer upstream.Close()

	if _, err := upstream.Write(p.Bytes); err != nil {
		return fmt.Errorf("forward: write request: %w", err)
	}

	return relayResponse(p.Conn, upstream)
}

// relayResponse reads the target's response head, writes it, then relays the
// body according to its framing so a keep-alive response still ends. The head
// is framed before it is written, so a malformed response fails while the
// caller can still answer with a 502; errors after the head is written are
// deliberately dropped (NFR-6).
func relayResponse(conn net.Conn, upstream net.Conn) error {
	r := bufio.NewReader(upstream)

	head, err := readHead(r)
	if err != nil {
		return err
	}
	contentLength, chunked, err := framing(head)
	if err != nil {
		return err
	}

	if _, err := conn.Write(head); err != nil {
		return fmt.Errorf("forward: write response head: %w", err)
	}

	switch {
	case chunked:
		// Relay each segment as it arrives, so a streamed response is not
		// batched (FR-15). A write error means the client left; the response
		// is already in flight, so stop silently.
		_ = walkChunks(r, nil, func(b []byte) error {
			_, werr := conn.Write(b)
			return werr
		})
	case contentLength >= 0:
		_, _ = io.CopyN(conn, r, contentLength)
	default:
		// No length and not chunked: the body ends when the target closes.
		_, _ = io.Copy(conn, r)
	}
	return nil
}
