// Command loadgen posts checkout orders to the demo's checkout service at a steady rate and logs a one-line summary of
// the responses, by status, every interval.
//
// Environment:
//
//	CHECKOUT_URL  base URL of checkout (default http://checkout.commerce.svc:8080); orders go to <CHECKOUT_URL>/v1/checkout
//	RATE          requests per second (default 5; a fraction such as 0.5 is allowed)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Config is what the load generator runs with.
type Config struct {
	// URL is where orders are posted: CHECKOUT_URL with /v1/checkout appended.
	URL string
	// Rate is the number of requests started per second.
	Rate float64
	// Interval is how often a summary line is logged.
	Interval time.Duration
	// MaxInFlight bounds the requests waiting for a response; a tick that finds it reached is counted as skipped.
	MaxInFlight int
	// Timeout bounds one request.
	Timeout time.Duration
}

const (
	defaultCheckoutURL = "http://checkout.commerce.svc:8080"
	defaultRate        = 5
	checkoutPath       = "/v1/checkout"
	maxRate            = 1000
)

// configFromEnv reads the Config from the environment through getenv.
func configFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		URL:         defaultCheckoutURL,
		Rate:        defaultRate,
		Interval:    10 * time.Second,
		MaxInFlight: 64,
		Timeout:     5 * time.Second,
	}
	if v := strings.TrimSpace(getenv("CHECKOUT_URL")); v != "" {
		cfg.URL = v
	}
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return Config{}, fmt.Errorf("CHECKOUT_URL %q: want an http:// or https:// URL", cfg.URL)
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/") + checkoutPath
	if v := strings.TrimSpace(getenv("RATE")); v != "" {
		r, err := strconv.ParseFloat(v, 64)
		if err != nil || r <= 0 || r > maxRate {
			return Config{}, fmt.Errorf("RATE %q: want a number of requests per second above 0 and at most %d", v, maxRate)
		}
		cfg.Rate = r
	}
	return cfg, nil
}

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Printf("posting to %s at %g req/s, summary every %s", cfg.URL, cfg.Rate, cfg.Interval)
	client := &http.Client{Timeout: cfg.Timeout}
	if err := run(ctx, cfg, client, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatalf("run: %v", err)
	}
}
