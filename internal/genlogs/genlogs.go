// Package genlogs writes synthetic JSONL application logs, for tests and for
// trying logi on realistic input.
//
// The output has nested objects, an array field, keys that appear on only
// some lines, about 2% out-of-order timestamps and about 0.1% lines that
// aren't JSON.
package genlogs

import (
	"bufio"
	"encoding/json/v2"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"time"
	"uuid"
)

// Options controls what Write produces.
type Options struct {
	// Size is the approximate number of bytes to write. Write stops at the
	// first line boundary past Size.
	Size int64
	// Seed makes the output deterministic, except for request IDs.
	Seed uint64
	// Start is the timestamp of the first line. The zero value means
	// 2026-11-20T00:00:00Z.
	Start time.Time
}

type httpInfo struct {
	Method    string  `json:"method"`
	Path      string  `json:"path"`
	Status    int     `json:"status"`
	LatencyMS float64 `json:"latency_ms"`
	Bytes     int     `json:"bytes"`
}

type userInfo struct {
	ID   int    `json:"id"`
	Plan string `json:"plan"`
}

type errInfo struct {
	Type  string `json:"type"`
	Stack string `json:"stack"`
}

type dbInfo struct {
	Query      string  `json:"query"`
	Rows       int     `json:"rows"`
	DurationMS float64 `json:"duration_ms"`
}

type entry struct {
	Time      string    `json:"time"`
	Level     string    `json:"level"`
	Msg       string    `json:"msg"`
	Service   string    `json:"service"`
	Host      string    `json:"host"`
	HTTP      *httpInfo `json:"http,omitzero"`
	RequestID string    `json:"request_id"`
	User      *userInfo `json:"user,omitzero"`
	TraceID   string    `json:"trace_id,omitzero"`
	SpanID    string    `json:"span_id,omitzero"`
	Error     *errInfo  `json:"error,omitzero"`
	DB        *dbInfo   `json:"db,omitzero"`
	Tags      []string  `json:"tags,omitzero"`
}

var (
	services = []string{
		"api", "billing", "auth", "search", "checkout", "inventory",
		"notifications", "gateway", "reports", "users", "media", "jobs",
	}
	methods  = []string{"GET", "GET", "GET", "GET", "POST", "PUT", "DELETE"}
	statuses = []int{
		200, 200, 200, 200, 200, 200, 200, 200, 200, 200, 200, 200,
		200, 200, 201, 204, 301, 304, 400, 401, 403, 404, 404, 409,
		429, 500, 502, 503,
	}
	plans  = []string{"free", "free", "free", "pro", "pro", "enterprise"}
	errTys = []string{
		"TimeoutError", "ConnectionReset", "ValidationError",
		"NotFound", "PermissionDenied", "DeadlockDetected",
	}
	tagSet = []string{"canary", "beta", "eu-west", "us-east", "batch", "retry"}
	msgs   = []string{
		"request completed",
		"request started",
		"cache miss for key %s",
		"cache hit for key %s",
		"upstream timeout after %dms",
		"connection reset by peer",
		"user %d logged in",
		"user %d logged out",
		"payment authorized for order %d",
		"payment declined for order %d",
		"retrying job %d attempt %d",
		"rate limit exceeded for client %s",
		"slow query detected",
		"feature flag %s evaluated",
		"session expired",
		"websocket closed with code %d",
		"failed to decode payload",
		"scheduled task finished in %dms",
		"invalid token signature",
		"queue depth is %d",
	}
)

// Write writes synthetic log lines to w until about opts.Size bytes have
// been written.
func Write(w io.Writer, opts Options) error {
	r := rand.New(rand.NewPCG(opts.Seed, opts.Seed^0x9e3779b97f4a7c15))
	bw := bufio.NewWriterSize(w, 1<<20)
	t := opts.Start
	if t.IsZero() {
		t = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	}

	stack := strings.Repeat("at handler.go:42\\n", 12)

	var written int64
	for written < opts.Size {
		t = t.Add(time.Duration(r.IntN(40_000)) * time.Microsecond)

		if r.IntN(1000) == 0 {
			n, err := fmt.Fprintf(bw, "panic: runtime error at %s\n",
				t.Format(time.RFC3339Nano))
			if err != nil {
				return fmt.Errorf("write plain line: %w", err)
			}

			written += int64(n)
			continue
		}

		b, err := json.Marshal(makeEntry(r, t, stack))
		if err != nil {
			return fmt.Errorf("marshal entry: %w", err)
		}

		b = append(b, '\n')
		n, err := bw.Write(b)
		if err != nil {
			return fmt.Errorf("write entry: %w", err)
		}

		written += int64(n)
	}

	err := bw.Flush()
	if err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	return nil
}

func makeEntry(r *rand.Rand, t time.Time, stack string) entry {
	ts := t
	if r.IntN(50) == 0 {
		ts = t.Add(-time.Duration(r.IntN(5000)) * time.Millisecond)
	}

	level := "info"
	switch p := r.IntN(100); {
	case p < 4:
		level = "error"
	case p < 12:
		level = "warn"
	case p < 22:
		level = "debug"
	}

	e := entry{
		Time:      ts.Format(time.RFC3339Nano),
		Level:     level,
		Msg:       makeMsg(r),
		Service:   services[r.IntN(len(services))],
		Host:      fmt.Sprintf("web-%02d", r.IntN(50)),
		RequestID: uuid.NewV4().String(),
	}

	if r.IntN(100) < 85 {
		e.HTTP = &httpInfo{
			Method:    methods[r.IntN(len(methods))],
			Path:      fmt.Sprintf("/api/v1/%s/%d", e.Service, r.IntN(500)),
			Status:    statuses[r.IntN(len(statuses))],
			LatencyMS: float64(r.IntN(200_000)) / 100,
			Bytes:     r.IntN(100_000),
		}
	}

	if r.IntN(100) < 70 {
		e.User = &userInfo{
			ID:   r.IntN(50_000) + 1,
			Plan: plans[r.IntN(len(plans))],
		}
	}

	if r.IntN(100) < 60 {
		e.TraceID = fmt.Sprintf("%016x%016x", r.Uint64(), r.Uint64())
		e.SpanID = fmt.Sprintf("%016x", r.Uint64())
	}

	if level == "error" {
		e.Error = &errInfo{
			Type:  errTys[r.IntN(len(errTys))],
			Stack: stack,
		}
	}

	if r.IntN(100) < 15 {
		e.DB = &dbInfo{
			Query:      "SELECT * FROM orders WHERE customer_id = $1",
			Rows:       r.IntN(1000),
			DurationMS: float64(r.IntN(50_000)) / 100,
		}
	}

	if r.IntN(100) < 10 {
		e.Tags = []string{
			tagSet[r.IntN(len(tagSet))],
			tagSet[r.IntN(len(tagSet))],
		}
	}

	return e
}

func makeMsg(r *rand.Rand) string {
	m := msgs[r.IntN(len(msgs))]
	n := strings.Count(m, "%")
	if n == 0 {
		return m
	}

	args := make([]any, n)
	verbs := strings.Split(m, "%")[1:]
	for i, v := range verbs {
		if strings.HasPrefix(v, "s") {
			args[i] = fmt.Sprintf("k%d", r.IntN(10_000))
			continue
		}

		args[i] = r.IntN(10_000)
	}

	return fmt.Sprintf(m, args...)
}
