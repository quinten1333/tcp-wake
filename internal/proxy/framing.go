package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"tcp-wake/internal/config"
)

// This file holds the request/response framing both Intake and the Forwarder
// relay share: reading a head, detecting the body/response framing from
// Content-Length or Transfer-Encoding, and walking a chunked body. Nothing here
// parses a request or a response into a typed object; the bytes are only
// counted so the boundary is known (ADR-0004).

// readHead reads header lines, including the blank line that ends them, and
// returns them as raw bytes. Reading line by line means the reader stops
// exactly at the body, with nothing read ahead.
func readHead(r *bufio.Reader) ([]byte, error) {
	var head []byte
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			head = append(head, line...)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: reading header: %v", ErrFraming, err)
		}
		if len(head) > maxHeadBytes {
			return nil, fmt.Errorf("%w: header block exceeds %d bytes", ErrFraming, maxHeadBytes)
		}
		if isBlankLine(line) {
			return head, nil
		}
	}
}

func isBlankLine(line []byte) bool {
	return bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n"))
}

// framing inspects only the two fields that decide where the body ends. Method,
// path, header order, header casing, and every other header are left as bytes.
func framing(head []byte) (contentLength int64, chunked bool, err error) {
	contentLength = -1
	lines := bytes.Split(head, []byte("\n"))
	for _, line := range lines[1:] {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			return 0, false, fmt.Errorf("%w: header line without a colon", ErrFraming)
		}
		name := strings.TrimSpace(string(line[:colon]))
		value := strings.TrimSpace(string(line[colon+1:]))

		switch {
		case strings.EqualFold(name, "Content-Length"):
			n, convErr := strconv.ParseInt(value, 10, 64)
			if convErr != nil || n < 0 {
				return 0, false, fmt.Errorf("%w: invalid Content-Length %q", ErrFraming, value)
			}
			if contentLength != -1 && contentLength != n {
				return 0, false, fmt.Errorf("%w: conflicting Content-Length values", ErrFraming)
			}
			contentLength = n
		case strings.EqualFold(name, "Transfer-Encoding"):
			for _, coding := range strings.Split(value, ",") {
				coding = strings.TrimSpace(coding)
				if coding == "" {
					continue
				}
				if !strings.EqualFold(coding, "chunked") {
					return 0, false, fmt.Errorf("%w: unsupported Transfer-Encoding %q", ErrFraming, coding)
				}
				chunked = true
			}
		}
	}
	if chunked {
		// Transfer-Encoding wins over Content-Length (RFC 9112 §6.1).
		contentLength = -1
	}
	return contentLength, chunked, nil
}

// appendExact reads exactly n body bytes, appending them verbatim.
func appendExact(r *bufio.Reader, raw *[]byte, n int64) error {
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return fmt.Errorf("%w: reading body: %v", ErrFraming, err)
	}
	*raw = append(*raw, body...)
	return nil
}

// readChunkedBody walks the chunk framing, appending every byte verbatim. The
// payload is never interpreted; only the size lines are read, because they are
// what says where the body ends. Body bytes are counted against the cap as they
// arrive, so at most the cap is ever buffered.
func readChunkedBody(r *bufio.Reader, raw *[]byte, limit config.ByteSize) error {
	var bodyBytes int64
	return walkChunks(r,
		func(size int64) error {
			if size > int64(limit)-bodyBytes {
				return ErrBodyTooLarge
			}
			bodyBytes += size
			return nil
		},
		func(b []byte) error {
			*raw = append(*raw, b...)
			return nil
		},
	)
}

// walkChunks reads a chunked body's framing from r. onSize is called with each
// chunk's declared size after its size line and may abort the walk (Intake uses
// it for the cap); emit is called with every raw byte in order — each size line,
// each data run, each CRLF, and the trailer section up to and including its
// blank line. The same walk therefore buffers a request under the cap (ADR-0004)
// and streams a response to the client (FR-15). A nil onSize accepts every size.
func walkChunks(r *bufio.Reader, onSize func(size int64) error, emit func([]byte) error) error {
	for {
		sizeLine, err := r.ReadBytes('\n')
		if len(sizeLine) > 0 {
			if emitErr := emit(sizeLine); emitErr != nil {
				return emitErr
			}
		}
		if err != nil {
			return fmt.Errorf("%w: reading chunk size: %v", ErrFraming, err)
		}
		size, err := parseChunkSize(sizeLine)
		if err != nil {
			return err
		}
		if size == 0 {
			return walkTrailers(r, emit)
		}
		if onSize != nil {
			if err := onSize(size); err != nil {
				return err
			}
		}

		chunk := make([]byte, size)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return fmt.Errorf("%w: reading chunk data: %v", ErrFraming, err)
		}
		if err := emit(chunk); err != nil {
			return err
		}

		crlf, err := r.ReadBytes('\n')
		if len(crlf) > 0 {
			if emitErr := emit(crlf); emitErr != nil {
				return emitErr
			}
		}
		if err != nil {
			return fmt.Errorf("%w: reading chunk terminator: %v", ErrFraming, err)
		}
	}
}

func parseChunkSize(line []byte) (int64, error) {
	s := strings.TrimSpace(string(line))
	if semi := strings.IndexByte(s, ';'); semi >= 0 {
		s = strings.TrimSpace(s[:semi]) // drop chunk extensions
	}
	n, err := strconv.ParseInt(s, 16, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: invalid chunk size %q", ErrFraming, s)
	}
	return n, nil
}

// walkTrailers consumes the trailer section that follows the final
// zero-length chunk, up to and including its blank line, emitting every byte.
func walkTrailers(r *bufio.Reader, emit func([]byte) error) error {
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if emitErr := emit(line); emitErr != nil {
				return emitErr
			}
		}
		if err != nil {
			return fmt.Errorf("%w: reading trailer: %v", ErrFraming, err)
		}
		if isBlankLine(line) {
			return nil
		}
	}
}
