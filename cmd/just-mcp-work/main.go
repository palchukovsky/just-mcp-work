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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
	"github.com/palchukovsky/just-mcp-work/internal/aiprofile"
	"github.com/palchukovsky/just-mcp-work/internal/mcpserver"
	"github.com/palchukovsky/just-mcp-work/internal/policy"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
	agentrunner "github.com/palchukovsky/just-mcp-work/internal/runner/agent"
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
		return printUsage(os.Stdout)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "init":
		return initCommand(args[1:])
	case "init-beta-test":
		return initBetaTestCommand(args[1:])
	case "version", "--version", "-version":
		fmt.Printf("just-mcp-work %s (%s)\n", version.Current().Display(), version.Commit)
		return nil
	case "help", "--help", "-h":
		return printUsage(os.Stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// serveOptions holds the resolved serve settings. Each duration takes its value
// from the flag, then the environment variable, then the built-in default.
//
//nolint:govet // Field order follows the documented flag order.
type serveOptions struct {
	Root              string
	RootExplicit      bool
	AIProfile         aiprofile.Profile
	Timeout           time.Duration
	TimeoutUnlimited  bool
	SyncDeadline      time.Duration
	Retention         time.Duration
	Exclude           []string
	RetiredRunnerMode bool
	HelpOnly          bool
	FlagsParsed       bool
}

func (options serveOptions) startupFailureOptions() map[string]string {
	if !options.FlagsParsed {
		return nil
	}
	result := map[string]string{
		"root_explicit":     strconv.FormatBool(options.RootExplicit),
		"timeout":           options.Timeout.String(),
		"timeout_unlimited": strconv.FormatBool(options.TimeoutUnlimited),
		"sync_deadline":     options.SyncDeadline.String(),
		"retention":         options.Retention.String(),
		"exclude":           strings.Join(options.Exclude, ","),
	}
	if options.AIProfile.ID != "" {
		result["ai_profile"] = options.AIProfile.ID
	}
	return result
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

func parseServeOptions(args []string) (serveOptions, string, error) {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	rootFromEnvironment := os.Getenv("JMW_ROOT") != ""
	root := flags.String("root", envOr("JMW_ROOT", "."), "workspace root")
	ai := flags.String("ai", "", "caller AI family: codex or claude")
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
	retiredRunnerMode := false
	flags.Func(
		"runner-mode",
		"retired; run just-mcp-work init to write the workspace runner policy",
		func(string) error {
			retiredRunnerMode = true
			return nil
		},
	)
	flags.Usage = func() {
		//nolint:errcheck // FlagSet usage callbacks cannot return output errors.
		// nosemgrep: discarded-error
		_, _ = fmt.Fprintln(
			flags.Output(),
			"Usage: just-mcp-work serve [--root <dir>] [--ai <codex|claude>] "+
				"[--timeout <duration>] "+
				"[--sync-deadline <duration>] [--retention <duration>] "+
				"[--exclude <glob>,...]",
		)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return serveOptions{HelpOnly: true}, *root, nil
		}
		return serveOptions{}, *root, fmt.Errorf("parse serve flags: %w", err)
	}
	rootExplicit := rootFromEnvironment
	aiExplicit := false
	flags.Visit(func(current *flag.Flag) {
		switch current.Name {
		case "root":
			rootExplicit = true
		case "ai":
			aiExplicit = true
		}
	})
	options := serveOptions{
		Root:              *root,
		RootExplicit:      rootExplicit,
		Timeout:           *timeout,
		TimeoutUnlimited:  *timeout == 0,
		SyncDeadline:      *syncDeadline,
		Retention:         *retention,
		Exclude:           splitCSV(*exclude),
		RetiredRunnerMode: retiredRunnerMode,
		FlagsParsed:       true,
	}
	if flags.NArg() != 0 {
		return options, *root, fmt.Errorf("serve accepts no positional arguments")
	}
	if *timeout < 0 {
		return options, *root, fmt.Errorf("timeout must not be negative")
	}
	if *timeout > 0 && *timeout < time.Millisecond {
		return options, *root, fmt.Errorf("timeout must be zero or at least 1ms")
	}
	if aiExplicit {
		declaredProfile, profileErr := aiprofile.Parse(*ai)
		if profileErr != nil {
			options.AIProfile = aiprofile.Profile{}
			return options, *root, fmt.Errorf("parse --ai: %w", profileErr)
		}
		options.AIProfile = declaredProfile
	}
	return options, *root, nil
}

func serve(args []string) (resultErr error) {
	options, startupRoot, err := parseServeOptions(args)
	startupOptions := options.startupFailureOptions()
	startupInProgress := true
	defer func() {
		if resultErr == nil || !startupInProgress {
			return
		}
		failure := runstore.StartupFailure{
			Time:    time.Now().UTC(),
			Version: version.Current().Display(),
			Commit:  version.Commit,
			Args:    append([]string(nil), args...),
			Root:    startupRoot,
			Options: startupOptions,
			Error:   resultErr.Error(),
		}
		if writeErr := runstore.WriteStartupFailure(startupRoot, failure); writeErr != nil {
			fmt.Fprintln(
				os.Stderr,
				"just-mcp-work: could not write startup failure record:",
				writeErr,
			)
		}
	}()
	if err != nil {
		return err
	}
	if options.HelpOnly {
		return nil
	}

	root, err := resolveServeRoot(options)
	if err != nil {
		return err
	}
	// Before resolution, failures use an already-parsed --root, else JMW_ROOT, else ".".
	// Afterwards they use root, so an earlier refusal may differ from what a later start clears.
	startupRoot = root
	if options.RetiredRunnerMode {
		return fmt.Errorf(
			"--runner-mode is no longer accepted by serve; the runner policy now lives in %s; "+
				"run just-mcp-work init to write it",
			policy.Path(root),
		)
	}
	managedSurfaces, verifyErr := agentinit.VerifyManagedSurfaces(root)
	if verifyErr != nil {
		return fmt.Errorf("verify managed surfaces: %w", verifyErr)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if options.AIProfile.Declared() {
		logger.Info(
			"AI profile selected",
			"ai_family", options.AIProfile.Family,
			"profile_id", options.AIProfile.ID,
			"profile_version", options.AIProfile.Version,
			"transport", options.AIProfile.Transport,
		)
	} else {
		logger.Info("AI profile not declared")
	}
	registry, err := runnerRegistry(root, logger)
	if err != nil {
		return err
	}
	workspaceRegistry, err := workspace.NewRegistry(root, registry, options.Exclude)
	if err != nil {
		return fmt.Errorf("create workspace registry: %w", err)
	}
	store, err := runstore.NewForWorktree(
		workspaceRegistry.Root(),
		workspaceRegistry.WorktreeRoot(),
	)
	if err != nil {
		return fmt.Errorf("create run store: %w", err)
	}
	server, err := mcpserver.New(
		workspaceRegistry,
		registry,
		store,
		mcpserver.Config{
			BetaTest:         managedSurfaces.BetaTest,
			AgentGuidePath:   managedSurfaces.AgentGuidePath,
			AIProfile:        options.AIProfile,
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
	startupInProgress = false
	// Stale-record removal is best-effort and must not prevent a successful startup.
	if removeErr := store.RemoveStartupFailure(); removeErr != nil {
		logger.Warn("could not remove stale startup failure", "error", removeErr)
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
		agentrunner.Registration("", ""),
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

func runnerRegistry(root string, logger *slog.Logger) (*runner.Registry, error) {
	workspacePolicy, err := policy.Load(root)
	if err != nil {
		return nil, fmt.Errorf("load runner policy: %w", err)
	}
	catalog, err := runnerCatalog()
	if err != nil {
		return nil, fmt.Errorf("create runner catalog: %w", err)
	}
	if !workspacePolicy.Found {
		logger.Warn(
			"runner policy is absent; no runner is enabled; run just-mcp-work init to write it",
			"policy_path",
			policy.Path(root),
		)
		registry, resolveErr := catalog.Resolve(catalog.DisabledSelections())
		if resolveErr != nil {
			return nil, fmt.Errorf(
				"resolve disabled runner modes: %w",
				resolveErr,
			)
		}
		return registry, nil
	}
	_, err = catalog.CompleteSelections(workspacePolicy.Selections)
	if err != nil {
		return nil, fmt.Errorf(
			"validate runner policy %s: %w",
			policy.Path(root),
			err,
		)
	}
	registry, err := catalog.Resolve(workspacePolicy.Selections)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve runner policy %s: %w",
			policy.Path(root),
			err,
		)
	}
	return registry, nil
}

func serverRunError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}
	return fmt.Errorf("run MCP server: %w", err)
}

func initCommand(args []string) error {
	return initCommandWithIO(false, args, os.Stdin, os.Stdout, os.Stderr)
}

func initBetaTestCommand(args []string) error {
	return initCommandWithIO(true, args, os.Stdin, os.Stdout, os.Stderr)
}

func parseInitPermissions(
	claudePermissionsFlag string,
	shellPermissionFlag string,
) (
	agentinit.ClaudePermissions,
	agentinit.ShellPermission,
	error,
) {
	permissions, err := agentinit.ParseClaudePermissions(claudePermissionsFlag)
	if err != nil {
		return "", "", fmt.Errorf("parse Claude permissions: %w", err)
	}
	if shellPermissionFlag == "" {
		return permissions, "", nil
	}
	shellPermission, err := agentinit.ParseShellPermission(shellPermissionFlag)
	if err != nil {
		return "", "", fmt.Errorf("parse shell permission: %w", err)
	}
	return permissions, shellPermission, nil
}

func initCommandWithIO(
	betaTest bool,
	args []string,
	input io.Reader,
	resultOutput io.Writer,
	diagnosticOutput io.Writer,
) error {
	command := "init"
	if betaTest {
		command = "init-beta-test"
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
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
	shellPermission := flags.String(
		"shell-permission",
		"",
		"shell tool handling in Claude permission lists and Codex approval modes: "+
			"allow or ask; empty asks on the console",
	)
	aiFamily := flags.String(
		"ai",
		"",
		"AI families for generated managed server arguments, one per generated "+
			"configuration: any of "+strings.Join(familyNames(aiprofile.Declarable()), ", ")+
			" separated by commas; empty asks on the console",
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
		// nosemgrep: discarded-error
		_, _ = fmt.Fprintln(
			flags.Output(),
			"Usage: just-mcp-work "+command+" [--dir <dir>] [--agents <names>] [--dry-run] "+
				"[--claude-permissions ask|yes|no] [--shell-permission allow|ask] "+
				"[--ai "+initAIFlagValues()+"] "+
				"[--runner-mode <name>=<mode>]...",
		)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse %s flags: %w", command, err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%s accepts no positional arguments", command)
	}
	permissions, parsedShellPermission, err := parseInitPermissions(
		*claudePermissions,
		*shellPermission,
	)
	if err != nil {
		return fmt.Errorf("parse %s flags: %w", command, err)
	}
	catalog, err := runnerCatalog()
	if err != nil {
		return fmt.Errorf("create runner catalog: %w", err)
	}
	console := initConsole{input: bufio.NewReader(input), output: diagnosticOutput}
	scope, err := agentinit.ResolveScope(*dir)
	if err != nil {
		return fmt.Errorf("resolve init scope: %w", err)
	}
	if !betaTest && !*dryRun {
		if confirmErr := console.confirmLeaveBetaTest(scope); confirmErr != nil {
			return confirmErr
		}
	}
	families, err := console.selectAIFamilies(scope, *aiFamily)
	if err != nil {
		return fmt.Errorf("select AI families: %w", err)
	}
	currentModes, err := console.currentRunnerModes(scope, catalog)
	if err != nil {
		return fmt.Errorf("read current runner modes: %w", err)
	}
	canonicalModes, err := console.selectRunnerModes(catalog, runnerModes, currentModes)
	if err != nil {
		return fmt.Errorf("select runner modes: %w", err)
	}
	selectedAgents := splitCSV(*agents)
	result, err := agentinit.Apply(
		agentinit.Options{
			Dir:               *dir,
			Agents:            selectedAgents,
			BetaTest:          betaTest,
			DryRun:            *dryRun,
			WriteMCPConfig:    *writeMCPConfig,
			AIFamilies:        families,
			RunnerModes:       canonicalModes,
			ClaudePermissions: permissions,
			ShellPermission:   parsedShellPermission,
			AskShellPermission: func(
				offer agentinit.ShellPermission,
				current bool,
			) (agentinit.ShellPermission, error) {
				return console.askShellPermission(singleOffer(string(offer), current))
			},
			Confirm: console.confirmClaudePermissions,
		},
	)
	if err != nil {
		return fmt.Errorf("apply agent instructions: %w", err)
	}
	return writeInitResult(resultOutput, result, *dryRun, *writeMCPConfig, families)
}

func writeInitResult(
	output io.Writer,
	result agentinit.Result,
	dryRun bool,
	writeMCPConfig bool,
	families aiprofile.Selection,
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
	snippet, snippetErr := agentinit.MCPConfigSnippet(result.Scope, families)
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

// parseInitAIFamilies reads the --ai value, which names as many families as the
// workspace declares, separated the same way the console answer is.
func parseInitAIFamilies(value string) (aiprofile.Selection, error) {
	families, err := aiprofile.ParseSelection(splitAnswerTokens(value))
	if err != nil {
		return nil, fmt.Errorf("parse AI families %q: %w", value, err)
	}
	return families, nil
}

// defaultAIFamilies is offered when no usable selection is recorded: every
// client this product generates a configuration for is declared.
func defaultAIFamilies() aiprofile.Selection {
	return aiprofile.Selection{aiprofile.FamilyCodex, aiprofile.FamilyClaude}
}

func (c *initConsole) selectAIFamilies(
	scope string,
	explicit string,
) (aiprofile.Selection, error) {
	if explicit != "" {
		return parseInitAIFamilies(explicit)
	}
	current, found, err := agentinit.ReadRecordedAIFamilies(scope)
	if errors.Is(err, agentinit.ErrUnrecognizedAIFamilies) {
		// A recorded selection this binary cannot read, such as one written by
		// an older release, is chosen again as in a new workspace. The refusal
		// carries the manifest it came from and what is wrong with it, so the
		// operator sees why the question offers the defaults.
		if writeErr := writeInitOutput(
			c.output,
			"\nThe AI families recorded by an earlier init cannot be used; "+
				"choose them again: %v\n",
			err,
		); writeErr != nil {
			return nil, writeErr
		}
		found, err = false, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read current AI families: %w", err)
	}
	offer := enumeratedOffer{values: familyNames(defaultAIFamilies())}
	if found {
		offer = enumeratedOffer{values: familyNames(current), current: true}
	}
	return c.askAIFamilies(offer)
}

func writeInitOutput(output io.Writer, format string, arguments ...any) error {
	if _, err := fmt.Fprintf(output, format, arguments...); err != nil {
		return fmt.Errorf("write init output: %w", err)
	}
	return nil
}

func (c *initConsole) confirmLeaveBetaTest(scope string) error {
	currentBetaTest, modeKnown, readErr := agentinit.ReadRecordedBetaTest(scope)
	if readErr != nil {
		return fmt.Errorf("read current beta-test mode: %w", readErr)
	}
	if modeKnown && !currentBetaTest {
		return nil
	}

	question := "This workspace is participating in the JMW beta test. Thank you for volunteering " +
		"to help improve JMW.\nLeave beta testing and continue with plain init? [y/N]: "
	if !modeKnown {
		question = "This workspace has a managed manifest, but its beta-test mode could not be read.\n" +
			"Plain init may remove beta feedback guidance. Continue with plain init? [y/N]: "
	}
	if err := writeInitOutput(c.output, "%s", question); err != nil {
		return err
	}
	answer, err := c.input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read leave-beta-test confirmation: %w", err)
	}
	trimmed := strings.TrimSpace(answer)
	if errors.Is(err, io.EOF) && trimmed == "" {
		if writeErr := writeInitOutput(
			c.output,
			"\nNo answer was given; the workspace is leaving beta testing. "+
				"Re-run the same command with init-beta-test in place of init to keep it.\n",
		); writeErr != nil {
			return writeErr
		}
		return nil
	}
	if strings.EqualFold(trimmed, "y") || strings.EqualFold(trimmed, "yes") {
		return nil
	}
	return fmt.Errorf(
		"init stopped; re-run the same command with init-beta-test in place of init " +
			"to keep this workspace in beta testing",
	)
}

func (c *initConsole) currentRunnerModes(
	scope string,
	catalog *runner.Catalog,
) (map[string]runner.Mode, error) {
	data, found, err := policy.Read(scope)
	if err != nil {
		return nil, fmt.Errorf("read runner policy %s: %w", policy.Path(scope), err)
	}
	if !found {
		return map[string]runner.Mode{}, nil
	}
	currentPolicy, err := policy.Parse(data)
	if err != nil {
		if writeErr := c.announcePolicyFallback(scope, err); writeErr != nil {
			return nil, writeErr
		}
		return map[string]runner.Mode{}, nil
	}
	current, changed, err := currentModesForCatalog(catalog, currentPolicy.Selections)
	if err != nil {
		if writeErr := c.announcePolicyFallback(scope, err); writeErr != nil {
			return nil, writeErr
		}
		return map[string]runner.Mode{}, nil
	}
	if changed {
		if writeErr := writeInitOutput(
			c.output,
			"Registered runner set changed; keeping current modes for matching runners, "+
				"using declared defaults for new runners, and dropping unregistered runners.\n",
		); writeErr != nil {
			return nil, writeErr
		}
	}
	return current, nil
}

func (c *initConsole) announcePolicyFallback(scope string, policyErr error) error {
	return writeInitOutput(
		c.output,
		"Existing runner policy %s could not be read; using declared defaults: %v\n",
		policy.Path(scope),
		policyErr,
	)
}

func currentModesForCatalog(
	catalog *runner.Catalog,
	selections []runner.Selection,
) (map[string]runner.Mode, bool, error) {
	requests := catalog.PermissionRequests()
	byName := make(map[string]runner.PermissionRequest, len(requests))
	for _, request := range requests {
		byName[request.Name] = request
	}
	current := make(map[string]runner.Mode, len(requests))
	changed := len(selections) != len(requests)
	for _, selection := range selections {
		request, registered := byName[selection.Name]
		if !registered {
			changed = true
			continue
		}
		if _, supported := findRequestedMode(request, string(selection.Mode)); !supported {
			return nil, false, fmt.Errorf(
				"runner %q has unsupported current mode %q",
				selection.Name,
				selection.Mode,
			)
		}
		current[selection.Name] = selection.Mode
	}
	if len(current) != len(requests) {
		changed = true
	}
	return current, changed, nil
}

func (c *initConsole) selectRunnerModes(
	catalog *runner.Catalog,
	overrides []runner.Selection,
	currentModes map[string]runner.Mode,
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
		offer := singleOffer(string(request.Default), false)
		if currentMode, found := currentModes[request.Name]; found {
			offer = singleOffer(string(currentMode), true)
		}
		mode, askErr := c.askRunnerMode(request, offer)
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

type enumeratedChoice struct {
	value       string
	description string
	warning     string
}

type enumeratedQuestion struct {
	introduction          string
	defaultValues         []string
	promptLabel           string
	readDescription       string
	unansweredDescription string
	flagName              string
	parse                 func(string) (string, bool)
	unsupported           func(string) error
	unsupportedPrompt     func(string) string
	choices               []enumeratedChoice
	// multiple accepts several choices in one answer. A single-choice question
	// refuses an answer that names more than one.
	multiple bool
}

// enumeratedOffer is the answer proposed for an unanswered question: the
// recorded choices when there are any, otherwise the question's default.
type enumeratedOffer struct {
	values  []string
	current bool
}

func singleOffer(value string, current bool) enumeratedOffer {
	return enumeratedOffer{values: []string{value}, current: current}
}

func (c *initConsole) askRunnerMode(
	request runner.PermissionRequest,
	offer enumeratedOffer,
) (runner.Mode, error) {
	review := "reviewed"
	if !request.Reviewed {
		review = "unreviewed"
	}
	choices := make([]enumeratedChoice, 0, len(request.Choices))
	for _, choice := range request.Choices {
		choices = append(choices, enumeratedChoice{
			value:       string(choice.Mode),
			description: choice.Label + ": " + choice.Description,
			warning:     choice.Warning,
		})
	}
	question := enumeratedQuestion{
		introduction: fmt.Sprintf(
			"\n%s runner (%s): %s\n%s\n",
			request.Name,
			review,
			request.Question,
			request.Context,
		),
		choices:               choices,
		defaultValues:         []string{string(request.Default)},
		promptLabel:           "Mode",
		readDescription:       request.Name + " runner mode",
		unansweredDescription: fmt.Sprintf("runner %q mode", request.Name),
		flagName:              fmt.Sprintf("--runner-mode %s=<mode>", request.Name),
		parse: func(value string) (string, bool) {
			mode, found := findRequestedMode(request, value)
			return string(mode), found
		},
		unsupported: func(value string) error {
			return fmt.Errorf("unsupported mode %q for runner %q", value, request.Name)
		},
		unsupportedPrompt: func(value string) string {
			return fmt.Sprintf(
				"Unsupported mode %q; choose one of %s.\n",
				value,
				requestModeNames(request),
			)
		},
	}
	values, err := c.askEnumeratedChoice(question, offer)
	if err != nil {
		return "", err
	}
	return runner.Mode(values[0]), nil
}

func (c *initConsole) askEnumeratedChoice(
	question enumeratedQuestion,
	offer enumeratedOffer,
) ([]string, error) {
	if err := writeInitOutput(c.output, "%s", question.introduction); err != nil {
		return nil, err
	}
	for index, choice := range question.choices {
		// Only a question answered with several choices numbers them, because
		// only its answer needs a short way to name more than one.
		number, warningIndent := "", "    "
		if question.multiple {
			number, warningIndent = fmt.Sprintf("%d) ", index+1), "     "
		}
		if err := writeInitOutput(
			c.output,
			"  %s%s%s - %s\n",
			number,
			choice.value,
			enumeratedChoiceLabel(choice.value, question.defaultValues, offer),
			choice.description,
		); err != nil {
			return nil, err
		}
		if choice.warning != "" {
			if err := writeInitOutput(
				c.output,
				"%sWARNING: %s\n",
				warningIndent,
				choice.warning,
			); err != nil {
				return nil, err
			}
		}
	}
	return c.readEnumeratedChoice(question, offer)
}

func enumeratedChoiceLabel(
	value string,
	defaults []string,
	offer enumeratedOffer,
) string {
	labels := make([]string, 0, 2)
	if offer.current && slices.Contains(offer.values, value) {
		labels = append(labels, "current")
	}
	if slices.Contains(defaults, value) {
		labels = append(labels, "default")
	}
	if len(labels) == 0 {
		return ""
	}
	return " (" + strings.Join(labels, ", ") + ")"
}

func (c *initConsole) readEnumeratedChoice(
	question enumeratedQuestion,
	offer enumeratedOffer,
) ([]string, error) {
	source := "default"
	if offer.current {
		source = "current"
	}
	for {
		if err := writeInitOutput(
			c.output,
			"%s [%s, %s]: ",
			question.promptLabel,
			strings.Join(offer.values, ","),
			source,
		); err != nil {
			return nil, err
		}
		answer, err := c.input.ReadString('\n')
		trimmed := strings.TrimSpace(answer)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read %s: %w", question.readDescription, err)
		}
		if trimmed == "" {
			if errors.Is(err, io.EOF) {
				if writeErr := writeInitOutput(c.output, "\n"); writeErr != nil {
					return nil, writeErr
				}
				return nil, fmt.Errorf(
					"%s was unanswered at end of input; use %s for non-interactive init",
					question.unansweredDescription,
					question.flagName,
				)
			}
			return offer.values, nil
		}
		values, prompt, answerErr := resolveEnumeratedAnswer(question, trimmed)
		if answerErr == nil {
			return values, nil
		}
		if errors.Is(err, io.EOF) {
			return nil, answerErr
		}
		if writeErr := writeInitOutput(c.output, "%s", prompt); writeErr != nil {
			return nil, writeErr
		}
	}
}

// resolveEnumeratedAnswer turns one typed answer into the values it names. It
// returns the console prompt for a rejected answer beside the error carrying
// the same rejection, so a closed console fails with the reason it would have
// printed. A single-choice question reads the whole answer as one name, so only
// a question answered with several choices splits it or accepts a number.
func resolveEnumeratedAnswer(
	question enumeratedQuestion,
	answer string,
) ([]string, string, error) {
	if !question.multiple {
		value, found := question.parse(answer)
		if !found {
			return nil, question.unsupportedPrompt(answer), question.unsupported(answer)
		}
		return []string{value}, "", nil
	}
	tokens := splitAnswerTokens(answer)
	if len(tokens) == 0 {
		return nil, question.unsupportedPrompt(answer), question.unsupported(answer)
	}
	values := make([]string, 0, len(tokens))
	for _, token := range tokens {
		value, found := resolveEnumeratedToken(question, token)
		if !found {
			return nil, question.unsupportedPrompt(token), question.unsupported(token)
		}
		if slices.Contains(values, value) {
			return nil, fmt.Sprintf("%q is named twice.\n", value), fmt.Errorf(
				"%q is named twice",
				value,
			)
		}
		values = append(values, value)
	}
	return values, "", nil
}

// resolveEnumeratedToken accepts either the number printed beside a choice or
// the choice's own name, so an operator never has to retype a name to answer.
func resolveEnumeratedToken(question enumeratedQuestion, token string) (string, bool) {
	if index, err := strconv.Atoi(token); err == nil {
		if index < 1 || index > len(question.choices) {
			return "", false
		}
		return question.choices[index-1].value, true
	}
	return question.parse(token)
}

// splitAnswerTokens cuts an answer on everything a choice name cannot contain,
// so numbers and names may be separated by commas, spaces, or both.
func splitAnswerTokens(answer string) []string {
	return strings.FieldsFunc(answer, func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '-' && char != '_'
	})
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

func (c *initConsole) askAIFamilies(offer enumeratedOffer) (aiprofile.Selection, error) {
	choices := make([]enumeratedChoice, 0, len(aiprofile.Declarable()))
	for _, family := range aiprofile.Declarable() {
		config, found := agentinit.AIFamilyConfig(family)
		if !found {
			return nil, fmt.Errorf("no generated configuration declares AI family %q", family)
		}
		choices = append(choices, enumeratedChoice{
			value:       string(family),
			description: "declare it in " + config,
		})
	}
	question := enumeratedQuestion{
		introduction: "\nWhich AI families should the managed just-mcp-work server declare?\n" +
			"Each family is declared in the configuration its own client reads, so a " +
			"workspace used by several of them names several; answer with the numbers " +
			"or the names, in any order.\n" +
			"This is recorded provenance and changes presentation only; runner and shell " +
			"permissions stay unchanged.\n",
		choices:               choices,
		defaultValues:         familyNames(defaultAIFamilies()),
		promptLabel:           "AI families",
		readDescription:       "AI families",
		unansweredDescription: "AI families",
		flagName:              "--ai " + initAIFlagValues(),
		multiple:              true,
		parse: func(value string) (string, bool) {
			if !slices.Contains(aiprofile.Declarable(), aiprofile.Family(value)) {
				return "", false
			}
			return value, true
		},
		unsupported: func(value string) error {
			return fmt.Errorf("unsupported AI family %q", value)
		},
		unsupportedPrompt: func(value string) string {
			return fmt.Sprintf(
				"Unsupported AI family %q; choose any of %s.\n",
				value,
				strings.Join(familyNames(aiprofile.Declarable()), ", "),
			)
		},
	}
	values, err := c.askEnumeratedChoice(question, offer)
	if err != nil {
		return nil, err
	}
	// The console already refused every answer this can reject, so a failure
	// here is a disagreement between the question and the family set itself.
	families, err := aiprofile.ParseSelection(values)
	if err != nil {
		return nil, fmt.Errorf("select AI families %v: %w", values, err)
	}
	return families, nil
}

func familyNames(families []aiprofile.Family) []string {
	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, string(family))
	}
	return names
}

// initAIFlagValues documents --ai the way it is answered: one family, or
// several separated by commas.
func initAIFlagValues() string {
	names := familyNames(aiprofile.Declarable())
	return strings.Join(names, "|") + "|" + strings.Join(names, ",")
}

func (c *initConsole) askShellPermission(
	offer enumeratedOffer,
) (agentinit.ShellPermission, error) {
	question := enumeratedQuestion{
		introduction: "\nHow should the Claude permission lists and Codex approval modes " +
			"handle the just-mcp-work shell tools?\n",
		choices: []enumeratedChoice{
			{
				value:       string(agentinit.ShellPermissionAllow),
				description: "use the Claude allow list and Codex approve mode",
			},
			{
				value:       string(agentinit.ShellPermissionAsk),
				description: "use the Claude ask list and Codex prompt mode",
			},
		},
		defaultValues:         []string{string(agentinit.ShellPermissionAsk)},
		promptLabel:           "Shell permission",
		readDescription:       "shell permission",
		unansweredDescription: "shell permission",
		flagName:              "--shell-permission allow|ask",
		parse: func(value string) (string, bool) {
			permission, err := agentinit.ParseShellPermission(value)
			return string(permission), err == nil
		},
		unsupported: func(value string) error {
			return fmt.Errorf("unsupported shell permission %q", value)
		},
		unsupportedPrompt: func(value string) string {
			return fmt.Sprintf(
				"Unsupported shell permission %q; choose one of allow, ask.\n",
				value,
			)
		},
	}
	values, err := c.askEnumeratedChoice(question, offer)
	if err != nil {
		return "", err
	}
	return agentinit.ShellPermission(values[0]), nil
}

// confirmClaudePermissions asks the operator on the same buffered console used
// for runner choices. Declining, an empty answer, or a closed console without an
// answer removes this server's managed permission entries and does not add them
// back; when nothing else was left in the settings file, the file itself is
// removed. A read failure that is not a plain end of input aborts instead.
func (c *initConsole) confirmClaudePermissions(
	shellPermission agentinit.ShellPermission,
	path string,
	_ string,
) (bool, error) {
	managed, err := agentinit.ClaudeManagedTools(shellPermission)
	if err != nil {
		return false, fmt.Errorf("resolve managed Claude tools: %w", err)
	}
	managedLists := "  allow: " + strings.Join(managed.Allow, ", ") + "\n"
	if len(managed.Ask) > 0 {
		managedLists += "  ask:   " + strings.Join(managed.Ask, ", ") + "\n"
	}
	if writeErr := writeInitOutput(
		c.output,
		"\n%s: apply the managed just-mcp-work tool permissions?\n"+
			"%s"+
			"Existing %s* entries are removed first; declining or leaving this empty\n"+
			"removes them, deleting the file if nothing else remains in it.\n"+
			"Apply? [y/N]: ",
		path,
		managedLists,
		agentinit.ClaudeToolPrefix,
	); writeErr != nil {
		return false, writeErr
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

func printUsage(output io.Writer) error {
	if _, err := fmt.Fprintln(output, "Usage: just-mcp-work <command> [options]"); err != nil {
		return fmt.Errorf("write usage: %w", err)
	}
	if _, err := fmt.Fprintln(output, "\nCommands:"); err != nil {
		return fmt.Errorf("write usage: %w", err)
	}
	if _, err := fmt.Fprintln(
		output,
		"  serve           Start the local STDIO MCP server",
	); err != nil {
		return fmt.Errorf("write usage: %w", err)
	}
	if _, err := fmt.Fprintln(
		output,
		"  init            Add managed task-server instructions for coding agents",
	); err != nil {
		return fmt.Errorf("write usage: %w", err)
	}
	if _, err := fmt.Fprintln(
		output,
		"  init-beta-test  Add managed instructions with JMW beta feedback guidance",
	); err != nil {
		return fmt.Errorf("write usage: %w", err)
	}
	if _, err := fmt.Fprintln(output, "  version         Print version and commit"); err != nil {
		return fmt.Errorf("write usage: %w", err)
	}
	return nil
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
