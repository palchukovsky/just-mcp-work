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
	aiExplicit := false
	flags.Visit(func(current *flag.Flag) {
		switch current.Name {
		case "root":
			rootExplicit = true
		case "ai":
			aiExplicit = true
		}
	})
	aiProfile := aiprofile.Unknown()
	if aiExplicit {
		declaredProfile, profileErr := aiprofile.Parse(*ai)
		if profileErr != nil {
			return serveOptions{}, fmt.Errorf("parse --ai: %w", profileErr)
		}
		aiProfile = declaredProfile
	}
	return serveOptions{
		Root:              *root,
		RootExplicit:      rootExplicit,
		AIProfile:         aiProfile,
		Timeout:           *timeout,
		TimeoutUnlimited:  *timeout == 0,
		SyncDeadline:      *syncDeadline,
		Retention:         *retention,
		Exclude:           splitCSV(*exclude),
		RetiredRunnerMode: retiredRunnerMode,
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

	root, err := resolveServeRoot(options)
	if err != nil {
		return err
	}
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
	logger.Info(
		"AI profile selected",
		"ai_family", options.AIProfile.Family,
		"profile_id", options.AIProfile.ID,
		"profile_version", options.AIProfile.Version,
		"transport", options.AIProfile.Transport,
	)
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
		"AI family for generated managed server arguments: unknown, codex, or claude; "+
			"empty asks on the console",
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
				"[--ai unknown|codex|claude] "+
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
	profile, err := console.selectAIProfile(scope, *aiFamily)
	if err != nil {
		return fmt.Errorf("select AI profile: %w", err)
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
			AIProfile:         profile,
			RunnerModes:       canonicalModes,
			ClaudePermissions: permissions,
			ShellPermission:   parsedShellPermission,
			AskShellPermission: func(
				offer agentinit.ShellPermission,
				current bool,
			) (agentinit.ShellPermission, error) {
				return console.askShellPermission(
					enumeratedOffer{value: string(offer), current: current},
				)
			},
			Confirm: console.confirmClaudePermissions,
		},
	)
	if err != nil {
		return fmt.Errorf("apply agent instructions: %w", err)
	}
	return writeInitResult(resultOutput, result, *dryRun, *writeMCPConfig, profile)
}

func writeInitResult(
	output io.Writer,
	result agentinit.Result,
	dryRun bool,
	writeMCPConfig bool,
	profile aiprofile.Profile,
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
	snippet, snippetErr := agentinit.MCPConfigSnippet(result.Scope, profile)
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

func parseInitAIProfile(value string) (aiprofile.Profile, error) {
	switch aiprofile.Family(value) {
	case aiprofile.FamilyUnknown:
		return aiprofile.Unknown(), nil
	case aiprofile.FamilyCodex, aiprofile.FamilyClaude:
		profile, err := aiprofile.Parse(value)
		if err != nil {
			return aiprofile.Profile{}, fmt.Errorf("parse AI family: %w", err)
		}
		return profile, nil
	default:
		return aiprofile.Profile{}, fmt.Errorf(
			"unsupported AI family %q; must be one of unknown, codex, claude",
			value,
		)
	}
}

func (c *initConsole) selectAIProfile(
	scope string,
	explicit string,
) (aiprofile.Profile, error) {
	if explicit != "" {
		return parseInitAIProfile(explicit)
	}
	current, found, err := agentinit.ReadRecordedAIProfile(scope)
	if err != nil {
		return aiprofile.Profile{}, fmt.Errorf("read current AI profile: %w", err)
	}
	offer := enumeratedOffer{value: string(aiprofile.FamilyUnknown)}
	if found {
		offer = enumeratedOffer{value: string(current.Family), current: true}
	}
	return c.askAIProfile(offer)
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
		offer := enumeratedOffer{value: string(request.Default)}
		if currentMode, found := currentModes[request.Name]; found {
			offer = enumeratedOffer{value: string(currentMode), current: true}
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
	defaultValue          string
	promptLabel           string
	readDescription       string
	unansweredDescription string
	flagName              string
	parse                 func(string) (string, bool)
	unsupported           func(string) error
	unsupportedPrompt     func(string) string
	choices               []enumeratedChoice
}

type enumeratedOffer struct {
	value   string
	current bool
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
		defaultValue:          string(request.Default),
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
	value, err := c.askEnumeratedChoice(question, offer)
	return runner.Mode(value), err
}

func (c *initConsole) askEnumeratedChoice(
	question enumeratedQuestion,
	offer enumeratedOffer,
) (string, error) {
	if err := writeInitOutput(c.output, "%s", question.introduction); err != nil {
		return "", err
	}
	for _, choice := range question.choices {
		if err := writeInitOutput(
			c.output,
			"  %s%s - %s\n",
			choice.value,
			enumeratedChoiceLabel(choice.value, question.defaultValue, offer),
			choice.description,
		); err != nil {
			return "", err
		}
		if choice.warning != "" {
			if err := writeInitOutput(c.output, "    WARNING: %s\n", choice.warning); err != nil {
				return "", err
			}
		}
	}
	return c.readEnumeratedChoice(question, offer)
}

func enumeratedChoiceLabel(
	value string,
	declaredDefault string,
	offer enumeratedOffer,
) string {
	labels := make([]string, 0, 2)
	if offer.current && value == offer.value {
		labels = append(labels, "current")
	}
	if value == declaredDefault {
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
) (string, error) {
	source := "default"
	if offer.current {
		source = "current"
	}
	for {
		if err := writeInitOutput(
			c.output,
			"%s [%s, %s]: ",
			question.promptLabel,
			offer.value,
			source,
		); err != nil {
			return "", err
		}
		answer, err := c.input.ReadString('\n')
		trimmed := strings.TrimSpace(answer)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read %s: %w", question.readDescription, err)
		}
		if trimmed == "" {
			if errors.Is(err, io.EOF) {
				if writeErr := writeInitOutput(c.output, "\n"); writeErr != nil {
					return "", writeErr
				}
				return "", fmt.Errorf(
					"%s was unanswered at end of input; use %s for non-interactive init",
					question.unansweredDescription,
					question.flagName,
				)
			}
			return offer.value, nil
		}
		if value, found := question.parse(trimmed); found {
			return value, nil
		}
		if errors.Is(err, io.EOF) {
			return "", question.unsupported(trimmed)
		}
		if writeErr := writeInitOutput(
			c.output,
			"%s",
			question.unsupportedPrompt(trimmed),
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

func (c *initConsole) askAIProfile(offer enumeratedOffer) (aiprofile.Profile, error) {
	question := enumeratedQuestion{
		introduction: "\nWhich AI family should the managed just-mcp-work server present?\n" +
			"This is recorded provenance and changes presentation only; runner and shell " +
			"permissions stay unchanged.\n",
		choices: []enumeratedChoice{
			{
				value:       string(aiprofile.FamilyUnknown),
				description: "do not declare an AI family",
			},
			{
				value:       string(aiprofile.FamilyCodex),
				description: "declare the Codex presentation profile",
			},
			{
				value:       string(aiprofile.FamilyClaude),
				description: "declare the Claude presentation profile",
			},
		},
		defaultValue:          string(aiprofile.FamilyUnknown),
		promptLabel:           "AI family",
		readDescription:       "AI family",
		unansweredDescription: "AI family",
		flagName:              "--ai unknown|codex|claude",
		parse: func(value string) (string, bool) {
			profile, err := parseInitAIProfile(value)
			return string(profile.Family), err == nil
		},
		unsupported: func(value string) error {
			return fmt.Errorf("unsupported AI family %q", value)
		},
		unsupportedPrompt: func(value string) string {
			return fmt.Sprintf(
				"Unsupported AI family %q; choose one of unknown, codex, claude.\n",
				value,
			)
		},
	}
	value, err := c.askEnumeratedChoice(question, offer)
	if err != nil {
		return aiprofile.Profile{}, err
	}
	return parseInitAIProfile(value)
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
		defaultValue:          string(agentinit.ShellPermissionAsk),
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
	value, err := c.askEnumeratedChoice(question, offer)
	return agentinit.ShellPermission(value), err
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
