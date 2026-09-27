package proxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"tcp-wake/internal/config"
)

// TestFR13IntakeRetainsBytesVerbatim proves FR-13's guarantee at the intake
// boundary: header casing, header order, odd whitespace, and the body are
// retained exactly, because the bytes are never reinterpreted (ADR-0004).
func TestFR13IntakeRetainsBytesVerbatim(t *testing.T) {
	req := "POST /v1/chat/completions?stream=1 HTTP/1.1\r\n" +
		"host: hypha.lan\r\n" +
		"CONTENT-length: 11\r\n" +
		"X-Odd-CASE:  Mixed   Value  \r\n" +
		"X-Order: first\r\n" +
		"X-Order: second\r\n" +
		"\r\n" +
		"hello world"

	p := intakeOnce(t, req, testCap)
	if got := string(p.Bytes); got != req {
		t.Fatalf("retained bytes differ\n got: %q\nwant: %q", got, req)
	}
	if p.Conn == nil {
		t.Fatal("retained request has no client connection")
	}
}

// TestFR13IntakeChunkedBodyVerbatim covers the chunked framing path, including
// a chunk extension and a trailer, retained byte for byte.
func TestFR13IntakeChunkedBodyVerbatim(t *testing.T) {
	req := "POST / HTTP/1.1\r\n" +
		"Host: hypha.lan\r\n" +
		"Transfer-Encoding: chunked\r\n" +
		"\r\n" +
		"5;ext=1\r\nhello\r\n" +
		"6\r\n world\r\n" +
		"0\r\nX-Trailer: yes\r\n\r\n"

	p := intakeOnce(t, req, testCap)
	if got := string(p.Bytes); got != req {
		t.Fatalf("retained chunked bytes differ\n got: %q\nwant: %q", got, req)
	}
}

// TestFR2IntakeGETCompletesAtHeadEnd covers a request with no body, which is
// complete at the header terminator.
func TestFR2IntakeGETCompletesAtHeadEnd(t *testing.T) {
	req := "GET /health HTTP/1.1\r\nHost: x\r\n\r\n"
	p := intakeOnce(t, req, testCap)
	if got := string(p.Bytes); got != req {
		t.Fatalf("retained bytes differ: got %q want %q", got, req)
	}
	if p.Arrival.IsZero() {
		t.Fatal("arrival not recorded")
	}
}

// TestFR20BodyAtCapIsAccepted checks the boundary: a body exactly at the cap is
// retained, and the cap is measured on body bytes, not the whole request.
func TestFR20BodyAtCapIsAccepted(t *testing.T) {
	body := "hello world" // 11 bytes
	req := "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 11\r\n\r\n" + body
	p := intakeOnce(t, req, config.ByteSize(len(body)))
	if got := string(p.Bytes); got != req {
		t.Fatalf("retained bytes differ: got %q want %q", got, req)
	}
}

// TestFR20BodyOverCapGets413 triggers both framing paths over the cap and
// asserts the 413 body names the held body cap and carries the limit
// (FR-20, ADR-0014).
func TestFR20BodyOverCapGets413(t *testing.T) {
	cases := []struct {
		name string
		head string
	}{
		{
			"content-length",
			"POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 11\r\n\r\n",
		},
		{
			"chunked",
			"POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n100000\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go io.WriteString(client, tc.head)

			errCh := make(chan error, 1)
			go func() {
				_, err := Intake(server, bufio.NewReader(server), 10)
				errCh <- err
			}()

			// A regression that stops rejecting an over-cap request would
			// otherwise block here forever waiting for the body.
			_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
			resp, err := io.ReadAll(client)
			if err != nil {
				t.Fatalf("reading response: %v", err)
			}
			if err := <-errCh; !errors.Is(err, ErrBodyTooLarge) {
				t.Fatalf("Intake error = %v, want ErrBodyTooLarge", err)
			}

			status, detail := parseErrorResponse(t, resp)
			if status != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", status)
			}
			if detail.Component != "held_body_cap" {
				t.Errorf("component = %q, want held_body_cap", detail.Component)
			}
			if detail.Limit != "10B" {
				t.Errorf("limit = %q, want the configured cap 10B", detail.Limit)
			}
			if !strings.Contains(detail.Message, "10B") {
				t.Errorf("message %q does not name the cap", detail.Message)
			}
		})
	}
}

// TestIntakeFramingErrorClosesWithoutResponse covers malformed framing: no
// response is synthesised, because the four JSON bodies are the only responses
// the system produces (ADR-0009).
func TestIntakeFramingErrorClosesWithoutResponse(t *testing.T) {
	cases := []struct {
		name string
		head string
	}{
		{"truncated-head", "GET / HTTP/1.1\r\nHost: x\r\n"},
		{"bad-content-length", "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: abc\r\n\r\n"},
		{"conflicting-content-length", "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 1\r\nContent-Length: 2\r\n\r\n"},
		{"header-without-colon", "GET / HTTP/1.1\r\nBroken header\r\n\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go func() {
				io.WriteString(client, tc.head)
				client.Close()
			}()

			_, err := Intake(server, bufio.NewReader(server), testCap)
			if !errors.Is(err, ErrFraming) {
				t.Fatalf("Intake error = %v, want ErrFraming", err)
			}
			server.Close()

			data, _ := io.ReadAll(client)
			if len(data) != 0 {
				t.Fatalf("got %d response bytes on a framing error: %q", len(data), data)
			}
		})
	}
}

// parseErrorResponse parses a response the proxy wrote itself and asserts the
// framing all four ADR-0009 bodies share: exactly one status line, a JSON
// content type, a Content-Length matching the body, and Connection: close. A
// test then asserts only the status and the ADR-0014 fields that distinguish
// its condition.
func parseErrorResponse(t *testing.T, resp []byte) (int, errorDetail) {
	t.Helper()
	head, body, ok := strings.Cut(string(resp), "\r\n\r\n")
	if !ok {
		t.Fatalf("response has no header terminator: %q", resp)
	}
	if got := strings.Count(head, "HTTP/1.1 "); got != 1 {
		t.Fatalf("found %d status lines, want exactly 1:\n%s", got, head)
	}
	var status int
	if _, err := fmt.Sscanf(head, "HTTP/1.1 %d", &status); err != nil {
		t.Fatalf("cannot parse status line %q: %v", head, err)
	}
	if !strings.Contains(head, "Content-Type: application/json") {
		t.Errorf("response does not declare its JSON type:\n%s", head)
	}
	if !strings.Contains(head, "Content-Length: "+strconv.Itoa(len(body))) {
		t.Errorf("Content-Length does not match the %d-byte body:\n%s", len(body), head)
	}
	if !strings.Contains(head, "Connection: close") {
		t.Errorf("response does not close the connection:\n%s", head)
	}
	var env errorEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("response body is not the ADR-0014 JSON: %q", body)
	}
	return status, env.Error
}
