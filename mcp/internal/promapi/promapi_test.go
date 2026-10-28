package promapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serve(t *testing.T, body string, status int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "" {
			http.Error(w, "no query", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, srv.Client())
}

func TestQuery(t *testing.T) {
	tests := map[string]struct {
		body  string
		value float64
		ok    bool
	}{
		"vector": {`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1793215243.5,"0.804"]}]}}`, 0.804, true},
		"scalar": {`{"status":"success","data":{"resultType":"scalar","result":[1793215243.5,"2"]}}`, 2, true},
		"empty":  {`{"status":"success","data":{"resultType":"vector","result":[]}}`, 0, false},
		"NaN":    {`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1793215243.5,"NaN"]}]}}`, 0, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			v, ok, err := serve(t, tc.body, http.StatusOK).Query(context.Background(), "up", time.Now())
			if err != nil || ok != tc.ok || v != tc.value {
				t.Errorf("Query = %v, %v, %v; want %v, %v", v, ok, err, tc.value, tc.ok)
			}
		})
	}
}

func TestQueryError(t *testing.T) {
	c := serve(t, `{"status":"error","errorType":"bad_data","error":"parse error"}`, http.StatusBadRequest)
	if _, _, err := c.Query(context.Background(), "up{", time.Now()); err == nil {
		t.Error("error response accepted")
	}
}

func TestQueryRange(t *testing.T) {
	c := serve(t, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1793215200,"0"],[1793215215,"NaN"],[1793215230,"0.5"]]}]}}`, http.StatusOK)
	got, err := c.QueryRange(context.Background(), "up", time.Unix(1793215200, 0), time.Unix(1793215230, 0), 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Value != 0.5 || !got[1].Time.Equal(time.Unix(1793215230, 0)) {
		t.Errorf("samples = %+v", got)
	}
}
