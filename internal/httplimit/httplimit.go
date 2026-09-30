// Package httplimit provides SEC-003's small, shared fetch-limit
// primitives — a size-bounded reader that fails with a typed error
// instead of silently truncating, and an HTTP client with a bounded
// redirect count — used at every point ragctl reads a response body from
// an untrusted external source (registry metadata, vanity-import
// resolution). Deliberately not a general resource-governance
// framework: two small helpers, not a new subsystem.
package httplimit

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrFetchLimitExceeded is returned when a fetch exceeds its configured
// size or redirect limit — a permanent, non-retryable condition for that
// fetch, never silently truncated or ignored.
var ErrFetchLimitExceeded = errors.New("fetch limit exceeded")

// ReadLimited reads at most max bytes from r, returning
// ErrFetchLimitExceeded if more than max bytes were available — unlike a
// bare io.LimitReader, which silently truncates, this makes an oversized
// response a clean, explicit, typed failure.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: response exceeded %d bytes", ErrFetchLimitExceeded, max)
	}
	return data, nil
}

// Client returns an *http.Client that fails a request once it would
// follow more than maxRedirects hops, instead of Go's default (a
// hardcoded 10) or an unbounded custom CheckRedirect.
func Client(base *http.Client, maxRedirects int) *http.Client {
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("%w: more than %d redirects", ErrFetchLimitExceeded, maxRedirects)
		}
		return nil
	}
	return &c
}
