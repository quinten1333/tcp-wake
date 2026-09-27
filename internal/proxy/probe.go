package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// readyBody is the exact health-endpoint body that means ready (IF-3). The only
// tolerance is surrounding whitespace, so a trailing newline from the origin
// does not read as not ready.
const readyBody = `{"status":"ok"}`

// probeBodyLimit bounds how much of the health response is read: enough for the
// ready body and a short error body, not enough for a misbehaving endpoint to
// grow the probe's memory.
const probeBodyLimit = 256

// httpProbe returns the observation for one health endpoint: it GETs
// target+path and reports ready only when the endpoint answers HTTP 200 with
// body {"status":"ok"} within timeout (FR-10, IF-3). Any other status, any
// other body, and any transport error or timeout mean not healthy (FR-11). The
// client disables keep-alives because the target may be mid-boot and a reused
// socket would be misleading.
func httpProbe(target, path string, timeout time.Duration) func(context.Context) bool {
	url := strings.TrimRight(target, "/") + path
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	return func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return false
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
		if err != nil {
			return false
		}
		return strings.TrimSpace(string(body)) == readyBody
	}
}
