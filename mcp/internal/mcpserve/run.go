package mcpserve

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register adds a server's tools to s once the command line is parsed. ctx bounds the server's
// background work and is done when the process is asked to stop.
type Register func(ctx context.Context, s *mcp.Server, logger *slog.Logger) error

// Main is the main function of one MCP server binary. fs is named after the binary, uses
// flag.ContinueOnError and holds the server's own flags; Main adds --addr and --version, parses
// the command line, calls register and serves until SIGINT or SIGTERM. On an error it prints it
// and exits 1.
func Main(fs *flag.FlagSet, version string, register Register) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, fs, os.Args[1:], os.Stderr, version, register); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", fs.Name(), err)
		stop()
		os.Exit(1)
	}
}

// Run is Main without the signal handling and the exit: it returns flag.ErrHelp after --help or
// --version, and any other error as is.
func Run(ctx context.Context, fs *flag.FlagSet, args []string, stderr io.Writer, version string, register Register) error {
	addr := fs.String("addr", ":8084", "listen address")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stderr, version)
		return flag.ErrHelp
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil)).With("server", fs.Name())
	s := mcp.NewServer(&mcp.Implementation{Name: fs.Name(), Version: version}, nil)
	if err := register(ctx, s, logger); err != nil {
		return err
	}
	return Serve(ctx, *addr, Handler(s, logger), logger)
}
