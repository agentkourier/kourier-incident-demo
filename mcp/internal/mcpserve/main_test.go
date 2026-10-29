package mcpserve

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRunVersion(t *testing.T) {
	var stderr bytes.Buffer
	called := false
	err := Run(context.Background(), flag.NewFlagSet("x-mcp", flag.ContinueOnError), []string{"--version"}, &stderr, "v1.2.3",
		func(context.Context, *mcp.Server, *slog.Logger) error { called = true; return nil })
	if !errors.Is(err, flag.ErrHelp) || strings.TrimSpace(stderr.String()) != "v1.2.3" || called {
		t.Errorf("err=%v stderr=%q called=%v", err, stderr.String(), called)
	}
}

func TestRunRejects(t *testing.T) {
	boom := errors.New("boom")
	register := func(context.Context, *mcp.Server, *slog.Logger) error { return boom }
	for name, args := range map[string][]string{
		"unknown flag":   {"--server=pager"},
		"register error": {"--addr=127.0.0.1:0"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := Run(context.Background(), flag.NewFlagSet("x-mcp", flag.ContinueOnError), args, &stderr, "dev", register)
			if err == nil {
				t.Fatal("Run succeeded")
			}
			if name == "register error" && !errors.Is(err, boom) {
				t.Errorf("err = %v, want the register error", err)
			}
		})
	}
}
