// Package mcpserve serves an MCP server over Streamable HTTP at /mcp, with /healthz for probes.
package mcpserve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Path is where the MCP endpoint is served.
const Path = "/mcp"

// Handler returns the HTTP handler: Streamable HTTP MCP at Path and a liveness check at /healthz.
// The transport is stateless, so a server restart never strands a client's session.
func Handler(server *mcp.Server, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(Path, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, Logger: logger}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// Serve listens on addr until ctx is done, then shuts down gracefully.
func Serve(ctx context.Context, addr string, h http.Handler, logger *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("serving MCP", "addr", addr, "path", Path)
	select {
	case err := <-errc:
		return fmt.Errorf("listen %s: %w", addr, err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
