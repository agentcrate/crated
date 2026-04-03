// Package httpclient provides a pre-configured HTTP client for LLM API calls.
//
// It bundles the production requirements that every provider needs:
//   - Request timeouts (default 120s — LLMs can be slow)
//   - Response body size limits (default 10 MB — prevents OOM)
//   - Automatic retries with exponential backoff for transient errors
//   - Structured logging of requests and errors
//
// Usage:
//
//	client := httpclient.New(httpclient.Options{Timeout: 60 * time.Second})
//	resp, err := client.Do(req)
package httpclient

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// Default configuration values.
const (
	DefaultTimeout     = 120 * time.Second
	DefaultMaxBodySize = 10 << 20 // 10 MB
	DefaultMaxRetries  = 3

	// Backoff parameters for retry delays.
	baseBackoff    = 500 * time.Millisecond
	maxBackoff     = 30 * time.Second
	jitterFraction = 0.25
)

// retryableStatusCodes are HTTP status codes that warrant a retry.
var retryableStatusCodes = map[int]bool{
	http.StatusTooManyRequests:     true, // 429
	http.StatusInternalServerError: true, // 500
	http.StatusBadGateway:          true, // 502
	http.StatusServiceUnavailable:  true, // 503
	http.StatusGatewayTimeout:      true, // 504
}

// Options configures the HTTP client behavior.
type Options struct {
	// Timeout is the maximum duration for the entire request (default: 120s).
	Timeout time.Duration

	// MaxBodySize is the maximum response body size in bytes (default: 10 MB).
	// Responses larger than this are truncated and an error is returned.
	MaxBodySize int64

	// MaxRetries is the number of retry attempts for transient errors (default: 3).
	// MaxRetries of 0 defaults to DefaultMaxRetries (3). Use -1 to disable retries.
	MaxRetries int

	// Logger is the structured logger for request/error logging.
	// If nil, a default logger is used.
	Logger *slog.Logger
}

func (o *Options) withDefaults() Options {
	out := *o
	if out.Timeout == 0 {
		out.Timeout = DefaultTimeout
	}
	if out.MaxBodySize == 0 {
		out.MaxBodySize = DefaultMaxBodySize
	}
	if out.MaxRetries == 0 {
		out.MaxRetries = DefaultMaxRetries
	} else if out.MaxRetries < 0 {
		out.MaxRetries = 0
	}
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	return out
}

// Client wraps http.Client with production-ready defaults.
type Client struct {
	http         *http.Client
	streamClient *http.Client // lazily initialized for streaming requests
	streamOnce   sync.Once
	opts         Options
	logger       *slog.Logger
}

// New creates a Client with the given options.
func New(opts Options) *Client {
	opts = opts.withDefaults()
	return &Client{
		http: &http.Client{
			Timeout: opts.Timeout,
		},
		opts:   opts,
		logger: opts.Logger,
	}
}

// Do executes an HTTP request with retries for transient failures.
// The caller is responsible for closing the response body.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// Buffer the request body so it can be replayed on retries.
	var bodyBytes []byte
	if req.Body != nil && req.GetBody == nil && c.opts.MaxRetries > 0 {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("buffering request body for retries: %w", err)
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	var lastErr error
	var lastResp *http.Response

	for attempt := 0; attempt <= c.opts.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt)
			c.logger.Warn("retrying request",
				"attempt", attempt,
				"delay", delay.String(),
				"url", req.URL.String(),
				"last_error", lastErr,
			)

			timer := time.NewTimer(delay)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			case <-timer.C:
			}

			// Reset the body for the retry.
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("resetting request body for retry: %w", err)
				}
				req.Body = body
			}
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			// Network errors are retryable.
			continue
		}

		// Non-retryable status — return immediately.
		if !retryableStatusCodes[resp.StatusCode] {
			return resp, nil
		}

		// Retryable status — drain body, log, and retry.
		lastResp = resp
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	if lastErr != nil {
		return nil, fmt.Errorf("request failed after %d attempts: %w", c.opts.MaxRetries+1, lastErr)
	}
	return lastResp, nil
}

// ReadBody reads the response body with a size limit.
// Returns an error if the body exceeds MaxBodySize.
func (c *Client) ReadBody(resp *http.Response) ([]byte, error) {
	lr := io.LimitReader(resp.Body, c.opts.MaxBodySize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if int64(len(data)) > c.opts.MaxBodySize {
		return nil, fmt.Errorf("response body exceeds %d bytes limit", c.opts.MaxBodySize)
	}
	return data, nil
}

// DoStream executes an HTTP request for streaming responses.
// Unlike Do, it does NOT retry (a partial stream cannot be retried) and uses
// no timeout (context cancellation controls the stream lifecycle instead).
// The caller MUST close resp.Body when done.
func (c *Client) DoStream(req *http.Request) (*http.Response, error) {
	// Use the client's transport but without timeout for streaming.
	c.streamOnce.Do(func() {
		c.streamClient = &http.Client{
			Transport: c.http.Transport,
		}
	})
	resp, err := c.streamClient.Do(req)
	if err != nil {
		return nil, err
	}

	// Check for error status before the caller starts consuming the stream.
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("stream request failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	return resp, nil
}

// backoff calculates exponential backoff with jitter.
func backoff(attempt int) time.Duration {
	delay := time.Duration(float64(baseBackoff) * math.Pow(2, float64(attempt-1)))
	if delay > maxBackoff {
		delay = maxBackoff
	}
	// Add jitter: ±25%
	jitter := time.Duration(rand.Int64N(int64(float64(delay)*jitterFraction*2))) - time.Duration(float64(delay)*jitterFraction)
	return delay + jitter
}
