package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Order is the body of POST /v1/checkout.
type Order struct {
	OrderID     string `json:"order_id"`
	AmountCents int    `json:"amount_cents"`
	Currency    string `json:"currency"`
}

const (
	minAmountCents = 500
	maxAmountCents = 20000
)

// newOrder returns an order with a random UUID-shaped ID and an amount between minAmountCents and maxAmountCents.
func newOrder() Order {
	return Order{
		OrderID:     newOrderID(),
		AmountCents: minAmountCents + mrand.IntN(maxAmountCents-minAmountCents+1),
		Currency:    "USD",
	}
}

// newOrderID returns a random version 4 UUID in its canonical form.
func newOrderID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Outcomes that are not an HTTP status code.
const (
	outcomeError   = "error"   // no response: connection refused, timeout, ...
	outcomeSkipped = "skipped" // not sent: MaxInFlight requests were already waiting
)

// tally counts outcomes (an HTTP status code such as "200", or outcomeError, or outcomeSkipped) since the last take.
type tally struct {
	mu     sync.Mutex
	counts map[string]int
	total  int
}

func (t *tally) add(outcome string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts == nil {
		t.counts = map[string]int{}
	}
	t.counts[outcome]++
	t.total++
}

// take returns the counts since the last take, the count of all outcomes ever added, and starts a new period.
func (t *tally) take() (map[string]int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.counts
	t.counts = map[string]int{}
	return c, t.total
}

// summary is the one-line summary of a period: "last 10s: 50 requests, 200=47 502=3 (total 1234)". Status codes come
// first in numeric order, then error and skipped.
func summary(period time.Duration, counts map[string]int, total int) string {
	keys := make([]string, 0, len(counts))
	n := 0
	for k, v := range counts {
		keys = append(keys, k)
		n += v
	}
	slices.SortFunc(keys, func(a, b string) int {
		ai, aerr := strconv.Atoi(a)
		bi, berr := strconv.Atoi(b)
		switch {
		case aerr == nil && berr == nil:
			return ai - bi
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			return strings.Compare(a, b)
		}
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	if len(parts) == 0 {
		parts = append(parts, "none")
	}
	return fmt.Sprintf("last %s: %d requests, %s (total %d)", period, n, strings.Join(parts, " "), total)
}

// post sends one order and returns its outcome.
func post(ctx context.Context, client *http.Client, url string) string {
	body, err := json.Marshal(newOrder())
	if err != nil {
		return outcomeError
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return outcomeError
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return outcomeError
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain, so the connection is reused
	return strconv.Itoa(resp.StatusCode)
}

// run posts orders at cfg.Rate until ctx is done, logging a summary every cfg.Interval and a last one on the way out.
// It returns ctx's error once the requests in flight have finished.
func run(ctx context.Context, cfg Config, client *http.Client, logger *log.Logger) error {
	var (
		t        tally
		wg       sync.WaitGroup
		inFlight = make(chan struct{}, cfg.MaxInFlight)
		every    = time.Duration(float64(time.Second) / cfg.Rate)
	)
	send := time.NewTicker(every)
	defer send.Stop()
	report := time.NewTicker(cfg.Interval)
	defer report.Stop()
	periodStart := time.Now()
	logSummary := func() {
		counts, total := t.take()
		logger.Print(summary(time.Since(periodStart).Round(time.Second), counts, total))
		periodStart = time.Now()
	}

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			logSummary()
			return ctx.Err()
		case <-report.C:
			logSummary()
		case <-send.C:
			select {
			case inFlight <- struct{}{}:
			default:
				t.add(outcomeSkipped)
				continue
			}
			wg.Go(func() {
				defer func() { <-inFlight }()
				// A request already sent when ctx is cancelled still gets its answer: shutdown waits for it.
				t.add(post(context.WithoutCancel(ctx), client, cfg.URL))
			})
		}
	}
}
