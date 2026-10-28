package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantURL string
		want    float64
		wantErr string
	}{
		{name: "defaults", env: nil, wantURL: "http://checkout.commerce.svc:8080/v1/checkout", want: 5},
		{name: "base URL with a trailing slash", env: map[string]string{"CHECKOUT_URL": "http://checkout:8080/"}, wantURL: "http://checkout:8080/v1/checkout", want: 5},
		{name: "fractional rate", env: map[string]string{"RATE": "0.5"}, wantURL: "http://checkout.commerce.svc:8080/v1/checkout", want: 0.5},
		{name: "rate 20", env: map[string]string{"RATE": " 20 "}, wantURL: "http://checkout.commerce.svc:8080/v1/checkout", want: 20},
		{name: "zero rate", env: map[string]string{"RATE": "0"}, wantErr: "RATE"},
		{name: "negative rate", env: map[string]string{"RATE": "-1"}, wantErr: "RATE"},
		{name: "rate not a number", env: map[string]string{"RATE": "fast"}, wantErr: "RATE"},
		{name: "rate too high", env: map[string]string{"RATE": "5000"}, wantErr: "RATE"},
		{name: "URL without scheme", env: map[string]string{"CHECKOUT_URL": "checkout:8080"}, wantErr: "CHECKOUT_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := configFromEnv(func(k string) string { return tt.env[k] })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("configFromEnv() error = %v, want one naming %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("configFromEnv() error = %v", err)
			}
			if cfg.URL != tt.wantURL || cfg.Rate != tt.want {
				t.Fatalf("configFromEnv() = URL %q rate %g, want %q %g", cfg.URL, cfg.Rate, tt.wantURL, tt.want)
			}
		})
	}
}

func TestNewOrder(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 5000 {
		o := newOrder()
		if !uuid.MatchString(o.OrderID) {
			t.Fatalf("order_id %q is not a v4 UUID", o.OrderID)
		}
		if seen[o.OrderID] {
			t.Fatalf("order_id %q repeated", o.OrderID)
		}
		seen[o.OrderID] = true
		if o.AmountCents < 500 || o.AmountCents > 20000 {
			t.Fatalf("amount_cents %d is outside 500-20000", o.AmountCents)
		}
		if o.Currency != "USD" {
			t.Fatalf("currency %q, want USD", o.Currency)
		}
	}
}

func TestOrderJSON(t *testing.T) {
	b, err := json.Marshal(Order{OrderID: "x", AmountCents: 1234, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"order_id":"x","amount_cents":1234,"currency":"USD"}`; got != want {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func TestSummary(t *testing.T) {
	tests := []struct {
		name   string
		counts map[string]int
		total  int
		want   string
	}{
		{name: "nothing", counts: map[string]int{}, total: 0, want: "last 10s: 0 requests, none (total 0)"},
		{
			name:   "codes in numeric order, then error and skipped",
			counts: map[string]int{"skipped": 1, "502": 3, "error": 2, "200": 47},
			total:  1234,
			want:   "last 10s: 53 requests, 200=47 502=3 error=2 skipped=1 (total 1234)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summary(10*time.Second, tt.counts, tt.total); got != tt.want {
				t.Fatalf("summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// syncBuffer is a bytes.Buffer safe for the logger and the test to share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRun runs the generator against a checkout that answers every third order 502 and checks the requests it got and the
// summaries it logged.
func TestRun(t *testing.T) {
	var n atomic.Int64
	var bad atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout" || r.Header.Get("Content-Type") != "application/json" {
			bad.Store(r.Method + " " + r.URL.Path + " " + r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		var o Order
		if err := json.Unmarshal(body, &o); err != nil || o.OrderID == "" || o.Currency != "USD" {
			bad.Store("body " + string(body))
		}
		if n.Add(1)%3 == 0 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg, err := configFromEnv(func(k string) string {
		return map[string]string{"CHECKOUT_URL": srv.URL, "RATE": "200"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Interval = 100 * time.Millisecond
	var out syncBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()
	if err := run(ctx, cfg, srv.Client(), log.New(&out, "", 0)); err != context.DeadlineExceeded {
		t.Fatalf("run() = %v, want context.DeadlineExceeded", err)
	}

	if v := bad.Load(); v != nil {
		t.Fatalf("checkout got a malformed request: %v", v)
	}
	got := n.Load()
	if got < 40 { // 200/s for 0.45s is 90; a slow machine still sends far more than 40
		t.Fatalf("checkout got %d requests, want about 90", got)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 4 {
		t.Fatalf("logged %d summary lines, want at least 4:\n%s", len(lines), out.String())
	}
	line := regexp.MustCompile(`^last \d+(\.\d+)?m?s: \d+ requests, (none|(200=\d+)?( ?502=\d+)?) \(total \d+\)$`)
	for _, l := range lines {
		if !line.MatchString(l) {
			t.Fatalf("summary line %q is not in the expected form", l)
		}
	}
	// The last summary's total is every request sent.
	last := lines[len(lines)-1]
	if !strings.HasSuffix(last, "(total "+strconv.FormatInt(got, 10)+")") {
		t.Fatalf("last summary %q, want total %d", last, got)
	}
}

// TestRunCountsErrors: a checkout that is not there is counted as error, not as a status.
func TestRunCountsErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more

	cfg, err := configFromEnv(func(k string) string { return map[string]string{"CHECKOUT_URL": url, "RATE": "50"}[k] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Interval = time.Hour
	var out syncBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = run(ctx, cfg, &http.Client{Timeout: time.Second}, log.New(&out, "", 0))
	if !regexp.MustCompile(` error=\d+ \(total \d+\)$`).MatchString(strings.TrimSpace(out.String())) {
		t.Fatalf("summary %q does not count errors", out.String())
	}
}
