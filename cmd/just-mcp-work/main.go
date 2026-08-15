// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	dockerrunner "github.com/palchukovsky/just-mcp-work/internal/runner/docker"
	gorunner "github.com/palchukovsky/just-mcp-work/internal/runner/go"
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

// serveOptions holds the resolved serve settings. Each duration takes its value
// from the flag, then the environment variable, then the built-in default.
//
//nolint:govet // Field order follows the documented flag order.
type serveOptions struct {
	Root             string
	RootExplicit     bool
	Timeout          time.Duration
	TimeoutUnlimited bool
	SyncDeadline     time.Duration
	Retention        time.Duration
	Exclude          []string
	RunnerModes      []runner.Selection
	HelpOnly         bool
}

type runnerModeFlag []runner.Selection

func (f *runnerModeFlag) String() string {
	values := make([]string, 0, len(*f))
	for _, selection := range *f {
		values = append(values, selection.Name+"="+string(selection.Mode))
	}
	return strings.Join(values, ",")
}

func (f *runnerModeFlag) Set(value string) error {
	name, mode, found := strings.Cut(value, "=")
	name = strings.TrimSpace(name)
	mode = strings.TrimSpace(mode)
	if !found || name == "" || mode == "" {
		return fmt.Errorf("runner mode must use <name>=<mode>")
	}
	*f = append(*f, runner.Selection{Name: name, Mode: runner.Mode(mode)})
	return nil
}

func parseServeOptions(args []string) (serveOptions, error) {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	rootFromEnvironment := os.Getenv("JMW_ROOT") != ""
	root := flags.String("root", envOr("JMW_ROOT", "."), "workspace root")
	timeout := flags.Duration(
		"timeout",
		durationEnvOr("JMW_TIMEOUT", 15*time.Minute),
		"per-task timeout",
	)
	syncDeadline := flags.Duration(
		"sync-deadline",
		durationEnvOr("JMW_SYNC_DEADLINE", time.Minute),
		"maximum synchronous wait before a run is promoted to the background",
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
	var runnerModes runnerModeFlag
	flags.Var(
		&runnerModes,
		"runner-mode",
		"runner permission mode as name=mode (case-sensitive); repeat for multiple runners",
	)
	flags.Usage = func() {
		//nolint:errcheck // FlagSet usage callbacks cannot return output errors.
		_, _ = fmt.Fprintln(
			flags.Output(),
			"Usage: just-mcp-work serve [--root <dir>] [--timeout <duration>] "+
				"[--sync-deadline <duration>] [--retention <duration>] "+
				"[--exclude <glob>,...] [--runner-mode <name>=<mode>]...",
		)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return serveOptions{HelpOnly: true}, nil
		}
		return serveOptions{}, fmt.Errorf("parse serve flags: %w", err)
	}
	if flags.NArg() != 0 {
		return serveOptions{}, fmt.Errorf("serve accepts no positional arguments")
	}
	if *timeout < 0 {
		return serveOptions{}, fmt.Errorf("timeout must not be negative")
	}
	if *timeout > 0 && *timeout < time.Millisecond {
		return serveOptions{}, fmt.Errorf("timeout must be zero or at least 1ms")
	}
	rootExplicit := rootFromEnvironment
	flags.Visit(func(current *flag.Flag) {
		if current.Name == "root" {
			rootExplicit = true
		}
	})
	return serveOptions{
		Root:             *root,
		RootExplicit:     rootExplicit,
		Timeout:          *timeout,
		TimeoutUnlimited: *timeout == 0,
		SyncDeadline:     *syncDeadline,
		Retention:        *retention,
		Exclude:          splitCSV(*exclude),
		RunnerModes:      runnerModes,
	}, nil
}

func serve(args []string) error {
	options, err := parseServeOptions(args)
	if err != nil {
		return err
	}
	if options.HelpOnly {
		return nil
	}

	catalog, err := runnerCatalog()
	if err != nil {
		return fmt.Errorf("create runner catalog: %w", err)
	}
	registry, err := catalog.Resolve(options.RunnerModes)
	if err != nil {
		return fmt.Errorf("resolve runner modes: %w", err)
	}
	root, err := resolveServeRoot(options)
	if err != nil {
		return err
	}
	workspaceRegistry, err := workspace.NewRegistry(root, registry, options.Exclude)
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
		mcpserver.Config{
			Timeout:          options.Timeout,
			TimeoutUnlimited: options.TimeoutUnlimited,
			SyncDeadline:     options.SyncDeadline,
			Retention:        options.Retention,
			Logger:           logger,
		},
	)
	if err != nil {
		return fmt.Errorf("create MCP server: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serverRunError(ctx, server.Run(ctx))
}

func resolveServeRoot(options serveOptions) (string, error) {
	if options.RootExplicit {
		return options.Root, nil
	}
	worktreeRoot, linked, err := workspace.ActiveWorktreeRoot(options.Root)
	if err != nil {
		return "", fmt.Errorf("resolve implicit workspace root: %w", err)
	}
	if linked {
		return worktreeRoot, nil
	}
	return options.Root, nil
}

// runnerCatalog is the shared production registration boundary used by serve
// now and by configuration flows that need the same declarations later.
func runnerCatalog() (*runner.Catalog, error) {
	catalog, err := runner.NewCatalog(
		justrunner.Registration(""),
		cmakerunner.Registration(""),
		dockerrunner.Registration(""),
		gorunner.Registration(""),
		makerunner.Registration(""),
	)
	if err != nil {
		return nil, fmt.Errorf("register production runners: %w", err)
	}
	return catalog, nil
}

func serverRunError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}
	return fmt.Errorf("run MCP server: %w", err)
}

func initCommand(args []string) error {
	return initCommandWithIO(args, os.Stdin, os.Stdout, os.Stderr)
}

func initCommandWithIO(
	args []string,
	input io.Reader,
	resultOutput io.Writer,
	diagnosticOutput io.Writer,
) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(diagnosticOutput)
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
		"true writes or rewrites the managed .mcp.json and Codex config entries; "+
			"false removes them and deletes a file left holding nothing else",
	)
	claudePermissions := flags.String(
		"claude-permissions",
		string(agentinit.ClaudePermissionsAsk),
		"managed tool permissions in .claude/settings.json, applied only when claude "+
			"is a selected agent: ask, yes to apply them, or no to remove them",
	)
	var runnerModes runnerModeFlag
	flags.Var(
		&runnerModes,
		"runner-mode",
		"runner permission mode as name=mode (case-sensitive); repeat to answer "+
			"runner questions up front",
	)
	flags.Usage = func() {
		//nolint:errcheck // FlagSet usage callbacks cannot return output errors.
		_, _ = fmt.Fprintln(
			flags.Output(),
			"Usage: just-mcp-work init [--dir <dir>] [--agents <names>] [--dry-run] "+
				"[--claude-permissions ask|yes|no] [--runner-mode <name>=<mode>]...",
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
	permissions, err := agentinit.ParseClaudePermissions(*claudePermissions)
	if err != nil {
		return fmt.Errorf("parse init flags: %w", err)
	}
	catalog, err := runnerCatalog()
	if err != nil {
		return fmt.Errorf("create runner catalog: %w", err)
	}
	console := initConsole{input: bufio.NewReader(input), output: diagnosticOutput}
	canonicalModes, err := console.selectRunnerModes(catalog, runnerModes)
	if err != nil {
		return fmt.Errorf("select runner modes: %w", err)
	}
	result, err := agentinit.Apply(
		agentinit.Options{
			Dir:               *dir,
			Agents:            splitCSV(*agents),
			DryRun:            *dryRun,
			WriteMCPConfig:    *writeMCPConfig,
			RunnerModes:       canonicalModes,
			ClaudePermissions: permissions,
			Confirm:           console.confirmClaudePermissions,
		},
	)
	if err != nil {
		return fmt.Errorf("apply agent instructions: %w", err)
	}
	return writeInitResult(resultOutput, result, *dryRun, *writeMCPConfig, canonicalModes)
}

func writeInitResult(
	output io.Writer,
	result agentinit.Result,
	dryRun bool,
	writeMCPConfig bool,
	canonicalModes runner.ValidatedSelections,
) error {
	if dryRun {
		for _, diff := range result.Diffs {
			if writeErr := writeInitOutput(output, "%s", diff); writeErr != nil {
				return writeErr
			}
		}
		return nil
	}
	if len(result.Paths) == 0 {
		if err := writeInitOutput(output, "Agent instructions are already up to date.\n"); err != nil {
			return err
		}
	} else {
		for _, path := range result.Paths {
			if writeErr := writeInitOutput(output, "Updated %s\n", path); writeErr != nil {
				return writeErr
			}
		}
	}
	if writeMCPConfig {
		return writeInitOutput(
			output,
			"Restart Codex or your MCP client to load updated server configuration.\n",
		)
	}
	snippet, snippetErr := agentinit.MCPConfigSnippet(canonicalModes)
	if snippetErr != nil {
		return fmt.Errorf("build MCP config snippet: %w", snippetErr)
	}
	if writeErr := writeInitOutput(
		output,
		"\nPaste this local MCP configuration if your agent does not discover it automatically:\n",
	); writeErr != nil {
		return writeErr
	}
	return writeInitOutput(output, "%s", snippet)
}

type initConsole struct {
	input  *bufio.Reader
	output io.Writer
}

func writeInitOutput(output io.Writer, format string, arguments ...any) error {
	if _, err := fmt.Fprintf(output, format, arguments...); err != nil {
		return fmt.Errorf("write init output: %w", err)
	}
	return nil
}

func (c *initConsole) selectRunnerModes(
	catalog *runner.Catalog,
	overrides []runner.Selection,
) (runner.ValidatedSelections, error) {
	validated, err := catalog.CanonicalSelections(overrides)
	if err != nil {
		return runner.ValidatedSelections{}, fmt.Errorf(
			"canonicalize runner mode overrides: %w",
			err,
		)
	}
	selections, err := validated.Selections()
	if err != nil {
		return runner.ValidatedSelections{}, fmt.Errorf("read canonical runner modes: %w", err)
	}
	overridden := make(map[string]struct{}, len(overrides))
	for _, selection := range overrides {
		overridden[selection.Name] = struct{}{}
	}
	for index, request := range catalog.PermissionRequests() {
		if _, found := overridden[request.Name]; found {
			continue
		}
		mode, askErr := c.askRunnerMode(request)
		if askErr != nil {
			return runner.ValidatedSelections{}, askErr
		}
		selections[index].Mode = mode
	}
	canonical, err := catalog.CanonicalSelections(selections)
	if err != nil {
		return runner.ValidatedSelections{}, fmt.Errorf("canonicalize selected runner modes: %w", err)
	}
	return canonical, nil
}

func (c *initConsole) askRunnerMode(request runner.PermissionRequest) (runner.Mode, error) {
	if err := c.writeRunnerModeRequest(request); err != nil {
		return "", err
	}
	return c.readRunnerMode(request)
}

func (c *initConsole) writeRunnerModeRequest(request runner.PermissionRequest) error {
	review := "reviewed"
	if !request.Reviewed {
		review = "unreviewed"
	}
	if err := writeInitOutput(
		c.output,
		"\n%s runner (%s): %s\n%s\n",
		request.Name,
		review,
		request.Question,
		request.Context,
	); err != nil {
		return err
	}
	for _, choice := range request.Choices {
		defaultLabel := ""
		if choice.Mode == request.Default {
			defaultLabel = " (default)"
		}
		if err := writeInitOutput(
			c.output,
			"  %s%s - %s: %s\n",
			choice.Mode,
			defaultLabel,
			choice.Label,
			choice.Description,
		); err != nil {
			return err
		}
		if choice.Warning != "" {
			if err := writeInitOutput(c.output, "    WARNING: %s\n", choice.Warning); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *initConsole) readRunnerMode(request runner.PermissionRequest) (runner.Mode, error) {
	for {
		if err := writeInitOutput(c.output, "Mode [%s]: ", request.Default); err != nil {
			return "", err
		}
		answer, err := c.input.ReadString('\n')
		trimmed := strings.TrimSpace(answer)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read %s runner mode: %w", request.Name, err)
		}
		if trimmed == "" {
			if errors.Is(err, io.EOF) {
				if writeErr := writeInitOutput(c.output, "\n"); writeErr != nil {
					return "", writeErr
				}
			}
			return request.Default, nil
		}
		if mode, found := findRequestedMode(request, trimmed); found {
			return mode, nil
		}
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("unsupported mode %q for runner %q", trimmed, request.Name)
		}
		if writeErr := writeInitOutput(
			c.output,
			"Unsupported mode %q; choose one of %s.\n",
			trimmed,
			requestModeNames(request),
		); writeErr != nil {
			return "", writeErr
		}
	}
}

func findRequestedMode(request runner.PermissionRequest, value string) (runner.Mode, bool) {
	for _, choice := range request.Choices {
		if value == string(choice.Mode) {
			return choice.Mode, true
		}
	}
	return "", false
}

func requestModeNames(request runner.PermissionRequest) string {
	names := make([]string, 0, len(request.Choices))
	for _, choice := range request.Choices {
		names = append(names, string(choice.Mode))
	}
	return strings.Join(names, ", ")
}

// confirmClaudePermissions asks the operator on the same buffered console used
// for runner choices. Declining, an empty answer, or a closed console without an
// answer removes this server's managed permission entries and does not add them
// back; when nothing else was left in the settings file, the file itself is
// removed. A read failure that is not a plain end of input aborts instead.
func (c *initConsole) confirmClaudePermissions(path string, _ string) (bool, error) {
	managed := agentinit.ClaudeManagedTools()
	if err := writeInitOutput(
		c.output,
		"\n%s: apply the managed just-mcp-work tool permissions?\n"+
			"  allow: %s\n  ask:   %s\n"+
			"Existing %s* entries are removed first; declining or leaving this empty\n"+
			"removes them, deleting the file if nothing else remains in it.\n"+
			"Apply? [y/N]: ",
		path,
		strings.Join(managed.Allow, ", "),
		strings.Join(managed.Ask, ", "),
		agentinit.ClaudeToolPrefix,
	); err != nil {
		return false, err
	}
	answer, err := c.input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read claude permissions confirmation for %s: %w", path, err)
	}
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		if writeErr := writeInitOutput(
			c.output,
			"\nNo answer; the managed entries are not applied, and any existing ones "+
				"are removed, including the file itself if nothing else was left in it. "+
				"Use --claude-permissions=yes to apply them, or --claude-permissions=no "+
				"to skip this prompt and remove them.\n",
		); writeErr != nil {
			return false, writeErr
		}
		return false, nil
	}
	switch strings.ToLower(trimmed) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
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
