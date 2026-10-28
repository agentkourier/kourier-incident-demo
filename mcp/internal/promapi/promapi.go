// Package promapi is a minimal Prometheus HTTP API client: instant and range queries that return
// one series. It reads only; it never writes to Prometheus.
package promapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxResponseBytes caps a response body.
const maxResponseBytes = 4 << 20

// Sample is one point of a series.
type Sample struct {
	Time  time.Time
	Value float64
}

// Client queries one Prometheus server.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for the Prometheus server at baseURL (for example
// http://prometheus-server.observability.svc). httpClient nil means a client with a 10s timeout.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

// Query runs an instant query at t and returns the first sample's value. ok is false when the
// result is empty or the value is NaN or infinite (for example a ratio over no traffic).
func (c *Client) Query(ctx context.Context, query string, t time.Time) (value float64, ok bool, err error) {
	form := url.Values{"query": {query}, "time": {formatTime(t)}}
	var data struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	if err := c.get(ctx, "/api/v1/query", form, &data); err != nil {
		return 0, false, err
	}
	var raw []any
	switch data.ResultType {
	case "scalar":
		if err := json.Unmarshal(data.Result, &raw); err != nil {
			return 0, false, fmt.Errorf("prometheus query: decode scalar: %w", err)
		}
	case "vector":
		var vec []struct {
			Value []any `json:"value"`
		}
		if err := json.Unmarshal(data.Result, &vec); err != nil {
			return 0, false, fmt.Errorf("prometheus query: decode vector: %w", err)
		}
		if len(vec) == 0 {
			return 0, false, nil
		}
		raw = vec[0].Value
	default:
		return 0, false, fmt.Errorf("prometheus query: unexpected result type %q", data.ResultType)
	}
	s, err := parsePoint(raw)
	if err != nil {
		return 0, false, err
	}
	if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
		return 0, false, nil
	}
	return s.Value, true, nil
}

// QueryRange runs a range query and returns the first series' samples, oldest first. NaN and
// infinite points are left out.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Sample, error) {
	form := url.Values{
		"query": {query},
		"start": {formatTime(start)},
		"end":   {formatTime(end)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	}
	var data struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Values [][]any `json:"values"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/api/v1/query_range", form, &data); err != nil {
		return nil, err
	}
	if data.ResultType != "matrix" {
		return nil, fmt.Errorf("prometheus query_range: unexpected result type %q", data.ResultType)
	}
	if len(data.Result) == 0 {
		return nil, nil
	}
	out := make([]Sample, 0, len(data.Result[0].Values))
	for _, p := range data.Result[0].Values {
		s, err := parsePoint(p)
		if err != nil {
			return nil, err
		}
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, form url.Values, data any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+form.Encode(), nil)
	if err != nil {
		return fmt.Errorf("prometheus %s: build request: %w", path, err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("prometheus %s: %w", path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("prometheus %s: read response: %w", path, err)
	}
	var envelope struct {
		Status string          `json:"status"`
		Error  string          `json:"error"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("prometheus %s: HTTP %d: decode response: %w", path, res.StatusCode, err)
	}
	if envelope.Status != "success" {
		return fmt.Errorf("prometheus %s: HTTP %d: %s", path, res.StatusCode, envelope.Error)
	}
	if err := json.Unmarshal(envelope.Data, data); err != nil {
		return fmt.Errorf("prometheus %s: decode data: %w", path, err)
	}
	return nil
}

// parsePoint reads a [unix seconds, "value"] pair.
func parsePoint(p []any) (Sample, error) {
	if len(p) != 2 {
		return Sample{}, fmt.Errorf("prometheus: malformed point %v", p)
	}
	ts, ok := p[0].(float64)
	if !ok {
		return Sample{}, fmt.Errorf("prometheus: malformed timestamp %v", p[0])
	}
	vs, ok := p[1].(string)
	if !ok {
		return Sample{}, fmt.Errorf("prometheus: malformed value %v", p[1])
	}
	v, err := strconv.ParseFloat(vs, 64)
	if err != nil {
		return Sample{}, fmt.Errorf("prometheus: value %q: %w", vs, err)
	}
	sec, frac := math.Modf(ts)
	return Sample{Time: time.Unix(int64(sec), int64(frac*1e9)).UTC(), Value: v}, nil
}

func formatTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
}
