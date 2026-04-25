package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/watcher"
	"github.com/jgravelle/gocodemunch-mcp/src/server"
)

const watcherEnvVar = "JCODEMUNCH_WATCH"

type serverRunner interface {
	Serve(ctx context.Context) error
}

type watcherBatchProcessorProvider interface {
	WatcherBatchProcessor() watcher.BatchProcessor
}

func main() {
	os.Exit(run())
}

func run() int {
	return runWithArgs(os.Args[1:])
}

func runWithArgs(args []string) int {
	cfg := config.Load()

	flags := flag.NewFlagSet("gocodemunch-mcp", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	var (
		showVersion = flags.Bool("version", false, "Print version and exit")
		transport   = flags.String("transport", "stdio", "Transport to run (stdio)")
		watcherFlag = flags.Bool("watcher", false, "Enable embedded watcher lifecycle worker")
		watcherPath = flags.String("watcher-path", "", "Comma-separated folder path(s) to watch (default: current directory)")
	)
	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Println(cfg.ServerVersion)
		return 0
	}

	if *transport != "stdio" {
		fmt.Fprintf(os.Stderr, "unsupported transport %q (only stdio is currently implemented)\n", *transport)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	watcherEnabled := resolveWatcherEnabled(args, *watcherFlag, os.Getenv(watcherEnvVar))
	options := []server.Option{
		server.WithServerInfo(cfg.ServerName, cfg.ServerVersion),
	}

	var watcherController watcher.Controller
	var watcherPaths []string
	if watcherEnabled {
		paths, err := resolveWatcherPaths(*watcherPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "watcher startup failed: %v\n", err)
			return 1
		}
		watcherPaths = paths
		watcherController = watcher.NewStateControllerWithStoragePath(cfg.StoragePath)
		options = append(options, server.WithWatcher(watcherController))
	}

	mcpServer := server.New(os.Stdin, os.Stdout, options...)

	var serveErr error
	if watcherEnabled {
		serveErr = runServerWithEmbeddedWatcher(ctx, mcpServer, watcherController, watcherPaths)
	} else {
		serveErr = mcpServer.Serve(ctx)
	}

	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		fmt.Fprintf(os.Stderr, "server failed: %v\n", serveErr)
		return 1
	}

	return 0
}

func runServerWithEmbeddedWatcher(ctx context.Context, srv serverRunner, controller watcher.Controller, paths []string) (err error) {
	options := []watcher.EmbeddedOption{}
	if provider, ok := srv.(watcherBatchProcessorProvider); ok {
		if processor := provider.WatcherBatchProcessor(); processor != nil {
			options = append(options, watcher.WithEmbeddedBatchProcessor(processor))
		}
	}

	worker, err := watcher.NewEmbeddedWorker(controller, paths, options...)
	if err != nil {
		return err
	}
	if err := worker.Start(ctx); err != nil {
		return err
	}

	defer func() {
		stopErr := worker.Stop(context.Background())
		if err == nil && stopErr != nil {
			err = stopErr
		}
	}()

	err = srv.Serve(ctx)
	return err
}

func resolveWatcherEnabled(rawArgs []string, cliFlag bool, envValue string) bool {
	if flagProvided(rawArgs, "watcher") {
		return cliFlag
	}
	return parseWatcherToggle(envValue)
}

func parseWatcherToggle(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return false
	}
	switch value {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func resolveWatcherPaths(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve current directory for watcher-path: %w", err)
		}
		return []string{cwd}, nil
	}

	parts := strings.Split(raw, ",")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		paths = append(paths, trimmed)
	}
	if len(paths) == 0 {
		return nil, errors.New("watcher-path requires at least one non-empty folder")
	}
	return paths, nil
}

func flagProvided(rawArgs []string, name string) bool {
	short := "-" + name
	long := "--" + name
	for _, arg := range rawArgs {
		switch {
		case arg == short, arg == long:
			return true
		case strings.HasPrefix(arg, short+"="), strings.HasPrefix(arg, long+"="):
			return true
		}
	}
	return false
}
