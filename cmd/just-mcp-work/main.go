// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
	"github.com/palchukovsky/just-mcp-work/internal/mcpserver"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
	cmakerunner "github.com/palchukovsky/just-mcp-work/internal/runner/cmake"
	justrunner "github.com/palchukovsky/just-mcp-work/internal/runner/just"
	makerunner "github.com/palchukovsky/just-mcp-work/internal/runner/make"
	"github.com/palchukovsky/just-mcp-work/internal/runstore"
	"github.com/palchukovsky/just-mcp-work/internal/version"
	"github.com/palchukovsky/just-mcp-work/internal/workspace"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "just-mcp-work:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "init":
		return initCommand(args[1:])
	case "version", "--version", "-version":
		fmt.Printf("just-mcp-work %s (%s)\n", version.Current().Display(), version.Commit)
		return nil
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("root", envOr("JMW_ROOT", "."), "workspace root")
	timeout := flags.Duration(
		"timeout",
		durationEnvOr("JMW_TIMEOUT", 15*time.Minute),
		"per-task timeout",
	)
	retention := flags.Duration(
		"retention",
		durationEnvOr("JMW_RETENTION", 72*time.Hour),
		"run-log retention",
	)
	exclude := flags.String(
		"exclude",
		"",
		"comma-separated directory names or relative glob patterns to skip",
	)
	flags.Usage = func() {
		//nolint:errcheck // FlagSet usage callbacks cannot return output errors.
		_, _ = fmt.Fprintln(
			flags.Output(),
			"Usage: just-mcp-work serve [--root <dir>] [--timeout <duration>] "+
				"[--retention <duration>] [--exclude <glob>,...]",
		)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse serve flags: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("serve accepts no positional arguments")
	}

	registry, err := runner.NewRegistry(
		justrunner.New(""),
		cmakerunner.New(""),
		makerunner.New(""),
	)
	if err != nil {
		return fmt.Errorf("create runner registry: %w", err)
	}
	workspaceRegistry, err := workspace.NewRegistry(*root, registry, splitCSV(*exclude))
	if err != nil {
		return fmt.Errorf("create workspace registry: %w", err)
	}
	store, err := runstore.New(workspaceRegistry.Root())
	if err != nil {
		return fmt.Errorf("create run store: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	server, err := mcpserver.New(
		workspaceRegistry,
		registry,
		store,
		mcpserver.Config{Timeout: *timeout, Retention: *retention, Logger: logger},
	)
	if err != nil {
		return fmt.Errorf("create MCP server: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serverRunError(ctx, server.Run(ctx))
}

func serverRunError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}
	return fmt.Errorf("run MCP server: %w", err)
}

func initCommand(args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)
	dir := flags.String("dir", ".", "workspace directory")
	agents := flags.String(
		"agents",
		"claude,codex,cursor",
		"comma-separated agent targets: claude,codex,cursor,copilot,windsurf",
	)
	dryRun := flags.Bool("dry-run", false, "print planned diffs without writing files")
	writeMCPConfig := flags.Bool(
		"write-mcp-config",
		true,
		"find the nearest .mcp.json and merge the server entry",
	)
	flags.Usage = func() {
		//nolint:errcheck // FlagSet usage callbacks cannot return output errors.
		_, _ = fmt.Fprintln(
			flags.Output(),
			"Usage: just-mcp-work init [--dir <dir>] [--agents <names>] [--dry-run]",
		)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse init flags: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("init accepts no positional arguments")
	}
	result, err := agentinit.Apply(
		agentinit.Options{
			Dir:            *dir,
			Agents:         splitCSV(*agents),
			DryRun:         *dryRun,
			WriteMCPConfig: *writeMCPConfig,
		},
	)
	if err != nil {
		return fmt.Errorf("apply agent instructions: %w", err)
	}
	if *dryRun {
		for _, diff := range result.Diffs {
			fmt.Print(diff)
		}
		return nil
	}
	if len(result.Paths) == 0 {
		fmt.Println("Agent instructions are already up to date.")
	} else {
		for _, path := range result.Paths {
			fmt.Println("Updated", path)
		}
	}
	if *writeMCPConfig {
		fmt.Println("Restart Codex or your MCP client to load updated server configuration.")
	}
	if !*writeMCPConfig {
		snippet, snippetErr := agentinit.MCPConfigSnippet()
		if snippetErr != nil {
			return fmt.Errorf("build MCP config snippet: %w", snippetErr)
		}
		fmt.Println(
			"\nPaste this local MCP configuration if your agent does not discover it automatically:",
		)
		fmt.Print(snippet)
	}
	return nil
}

func printUsage(output *os.File) {
	//nolint:errcheck // Usage output cannot be reported through this void helper.
	_, _ = fmt.Fprintln(output, "Usage: just-mcp-work <command> [options]")
	//nolint:errcheck // Usage output cannot be reported through this void helper.
	_, _ = fmt.Fprintln(output, "\nCommands:")
	//nolint:errcheck // Usage output cannot be reported through this void helper.
	_, _ = fmt.Fprintln(output, "  serve    Start the local STDIO MCP server")
	//nolint:errcheck // Usage output cannot be reported through this void helper.
	_, _ = fmt.Fprintln(output, "  init     Add managed task-server instructions for coding agents")
	//nolint:errcheck // Usage output cannot be reported through this void helper.
	_, _ = fmt.Fprintln(output, "  version  Print version and commit")
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationEnvOr(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return duration
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
