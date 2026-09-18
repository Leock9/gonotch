// Package sock reaches the running app over its Unix socket; the hook binary and the CLI share it.
package sock

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Client talks HTTP over the socket at path; the host part of every URL is ignored.
func Client(path string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
}

// URL is a request path on the app.
func URL(path string) string { return "http://gonotch" + path }
