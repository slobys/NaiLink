// SPDX-License-Identifier: AGPL-3.0-only
package dialer

import (
	"context"
	"io"
	"net/http"
	"time"
)

// HTTP/1 keep-alive requires reaching EOF before closing the response. Without
// this, small GET responses open a new proxy tunnel at every health check.
// Draining is only an opportunity to reuse the connection: large or stalled
// bodies must not turn a header-based probe into an unbounded download.
func routerFinishHealthResponse(response *http.Response, cancel context.CancelFunc) {
	defer response.Body.Close()
	const maxBody = 4096
	if response.Body == http.NoBody || response.ContentLength > maxBody {
		return
	}
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	// One extra byte distinguishes an unknown-length body at the limit from EOF.
	// A drain error does not change the existing HTTP status classification.
	_, _ = io.CopyN(io.Discard, response.Body, maxBody+1)
}
