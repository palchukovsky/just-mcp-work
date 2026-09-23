// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
	"github.com/palchukovsky/just-mcp-work/internal/aiprofile"
	"github.com/palchukovsky/just-mcp-work/internal/mcpserver"
	"github.com/palchukovsky/just-mcp-work/internal/policy"
	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
	"github.com/palchukovsky/just-mcp-work/internal/questionnaire/console"
	"github.com/palchukovsky/just-mcp-work/internal/questionnaire/form"
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	root, err := resolveServeRoot(options)
	if err != nil {
		return err
	}
	// Before resolution, failures use an already-parsed --root, else JMW_ROOT, else ".".
	// Afterwards they use root, so an earlier refusal may differ from what a later start clears.
	startupRoot = root
	stateRoot, managedSurfaces, err := resolveWorkspaceState(
		root,
		options.RetiredRunnerMode,
		logger,
	)
	if err != nil {
		return err
	}
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
	registry, policyExcludes, err := resolveWorkspacePolicy(stateRoot, logger)
	if err != nil {
		return err
	}
	workspaceRegistry, err := workspace.NewRegistry(
		root,
		registry,
		discoveryExclusions(stateRoot, policyExcludes, root, options.Exclude),
	)
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

// resolveWorkspaceState returns the directory holding the runner policy and the
// managed manifest for root, together with the surfaces verified there. init
// anchors both at the workspace boundary it resolves rather than at its --dir,
// so serve resolves the same boundary; a --root below that boundary would
// otherwise find neither and start with every runner disabled.
//
// The boundary never lies below root: without an anchoring .mcp.json above it,
// resolution returns root itself, so a served subtree keeps its own state. The
// served root still selects the discovered projects and the run store.
//
// The retired --runner-mode refusal is placed ahead of the verification on
// purpose: it answers a mistake in the command the operator just typed, which is
// more useful to them than a workspace-integrity error that command did not
// cause. It also needs the resolved policy path for its message. Verification
// stays ahead of the policy, so an edited managed block still stops the server
// before its runner selection is read.
func resolveWorkspaceState(
	root string,
	retiredRunnerMode bool,
	logger *slog.Logger,
) (string, agentinit.ManagedSurfaces, error) {
	stateRoot, err := agentinit.ResolveScope(root)
	if err != nil {
		return "", agentinit.ManagedSurfaces{}, fmt.Errorf("resolve workspace state root: %w", err)
	}
	// Logged unconditionally. This path is the answer to "which policy is this
	// server running", and deciding whether to mention it by comparing the
	// resolved path with an unnormalized --root announces a move on every
	// ordinary start, where --root defaults to ".".
	logger.Info("workspace state root resolved", "state_root", stateRoot)
	if retiredRunnerMode {
		return "", agentinit.ManagedSurfaces{}, fmt.Errorf(
			"--runner-mode is no longer accepted by serve; the runner policy now lives in %s; "+
				"run just-mcp-work init to write it",
			policy.Path(stateRoot),
		)
	}
	surfaces, err := agentinit.VerifyManagedSurfaces(stateRoot)
	if err != nil {
		return "", agentinit.ManagedSurfaces{}, fmt.Errorf("verify managed surfaces: %w", err)
	}
	return stateRoot, surfaces, nil
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

// discoveryExclusions pairs every pattern with the directory it was written
// for: the policy's with the state root init wrote it at, --exclude's with the
// served root, so a policy pattern keeps its meaning when serve runs on a
// subtree of the workspace.
func discoveryExclusions(
	stateRoot string,
	policyPatterns []string,
	root string,
	flagPatterns []string,
) []workspace.Exclusion {
	exclusions := make([]workspace.Exclusion, 0, len(policyPatterns)+len(flagPatterns))
	for _, pattern := range policyPatterns {
		exclusions = append(exclusions, workspace.Exclusion{Base: stateRoot, Pattern: pattern})
	}
	for _, pattern := range flagPatterns {
		exclusions = append(exclusions, workspace.Exclusion{Base: root, Pattern: pattern})
	}
	return exclusions
}

// resolveWorkspacePolicy reads the workspace policy at root once and returns
// the runners it enables together with the directory patterns project
// discovery skips. A policy written before init recorded exclusions skips
// none; an absent policy enables no runner and skips none.
func resolveWorkspacePolicy(
	root string,
	logger *slog.Logger,
) (*runner.Registry, []string, error) {
	workspacePolicy, err := policy.Load(root)
	if err != nil {
		return nil, nil, fmt.Errorf("load runner policy: %w", err)
	}
	catalog, err := runnerCatalog()
	if err != nil {
		return nil, nil, fmt.Errorf("create runner catalog: %w", err)
	}
	if !workspacePolicy.Found {
		logger.Warn(
			"runner policy is absent; no runner is enabled; run just-mcp-work init to write it",
			"policy_path",
			policy.Path(root),
		)
		registry, resolveErr := catalog.Resolve(catalog.DisabledSelections())
		if resolveErr != nil {
			return nil, nil, fmt.Errorf(
				"resolve disabled runner modes: %w",
				resolveErr,
			)
		}
		return registry, nil, nil
	}
	_, err = catalog.CompleteSelections(workspacePolicy.Selections)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"validate runner policy %s: %w",
			policy.Path(root),
			err,
		)
	}
	registry, err := catalog.Resolve(workspacePolicy.Selections)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"resolve runner policy %s: %w",
			policy.Path(root),
			err,
		)
	}
	return registry, workspacePolicy.Exclude.Patterns(), nil
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

func validateInstructionsAgents(
	target agentinit.InstructionsTarget,
	agentsValue string,
) error {
	if target != agentinit.InstructionsTargetMachine {
		return nil
	}
	var unsupported []string
	for _, agent := range splitCSV(agentsValue) {
		switch agent {
		case "claude", "codex", "windsurf":
		default:
			unsupported = append(unsupported, agent)
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	return fmt.Errorf(
		"instructions target machine cannot be used with --agents %q: selected agents "+
			"without a machine-wide instruction path: %s; re-run with "+
			"--agents claude,codex,windsurf",
		agentsValue,
		strings.Join(unsupported, ","),
	)
}

//nolint:gocyclo // Keep the required question order and the single Apply handoff explicit.
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
	betaTest := flags.Bool(
		"beta-test",
		false,
		"take part in the JMW beta test, which gives agents and clients JMW beta feedback "+
			"guidance; omitted, it is asked on the console",
	)
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
	instructionsTargetFlag := flags.String(
		"instructions-target",
		"",
		"agent-instruction destination: project|workspace|machine; empty asks on the console",
	)
	aiFamily := flags.String(
		"ai",
		"",
		"AI families for generated managed server arguments, one per generated "+
			"configuration: any of "+strings.Join(familyNames(aiprofile.Declarable()), ", ")+
			" separated by commas; empty asks on the console",
	)
	instructionsPointer := flags.Bool(
		"instructions-pointer",
		false,
		"write the managed instruction block as a pointer to the server's "+
			"instructions instead of the full contract",
	)
	var runnerModes runnerModeFlag
	flags.Var(
		&runnerModes,
		"runner-mode",
		"runner permission mode as name=mode (case-sensitive); repeat to answer "+
			"runner questions up front",
	)
	excludeMode := flags.String(
		"exclude-mode",
		"",
		"directories project discovery skips besides .git and .just-mcp-work: "+
			strings.Join(excludeModes(), ", ")+"; empty asks on the console",
	)
	exclude := flags.String(
		"exclude",
		"",
		"comma-separated directory names or slash-separated globs of your own for "+
			"discovery to skip, with --exclude-mode custom or both",
	)
	flags.Usage = func() {
		//nolint:errcheck // FlagSet usage callbacks cannot return output errors.
		// nosemgrep: discarded-error
		_, _ = fmt.Fprintln(
			flags.Output(),
			"Usage: just-mcp-work init [--dir <dir>] [--agents <names>] [--dry-run] "+
				"[--beta-test[=true|false]] "+
				"[--claude-permissions ask|yes|no] [--shell-permission allow|ask] "+
				"[--instructions-target project|workspace|machine] "+
				"[--ai "+initAIFlagValues()+"] "+
				"[--instructions-pointer] "+
				"[--runner-mode <name>=<mode>]... "+
				"[--exclude-mode "+strings.Join(excludeModes(), "|")+"] "+
				"[--exclude <pattern>,...]",
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
		return errors.New("init accepts no positional arguments")
	}
	permissions, parsedShellPermission, err := parseInitPermissions(
		*claudePermissions,
		*shellPermission,
	)
	if err != nil {
		return fmt.Errorf("parse init flags: %w", err)
	}
	var instructionsTarget agentinit.InstructionsTarget
	if *instructionsTargetFlag != "" {
		instructionsTarget, err = agentinit.ParseInstructionsTarget(*instructionsTargetFlag)
		if err != nil {
			return fmt.Errorf("parse init flags: %w", err)
		}
	}
	catalog, err := runnerCatalog()
	if err != nil {
		return fmt.Errorf("create runner catalog: %w", err)
	}
	scope, pointerInstructions, err := resolveInitInstructionsPointer(
		flags,
		*dir,
		*instructionsPointer,
	)
	if err != nil {
		return err
	}
	selectedAgents := splitCSV(*agents)
	if instructionsTarget != "" {
		if validateErr := validateInstructionsAgents(
			instructionsTarget,
			*agents,
		); validateErr != nil {
			return fmt.Errorf("validate init flags: %w", validateErr)
		}
	}
	var families aiprofile.Selection
	if *aiFamily != "" {
		families, err = parseInitAIFamilies(*aiFamily)
		if err != nil {
			return fmt.Errorf("select AI families: %w", err)
		}
	}
	overrides, err := catalog.CanonicalSelections(runnerModes)
	if err != nil {
		return fmt.Errorf("select runner modes: canonicalize runner mode overrides: %w", err)
	}
	overridden := make(map[string]struct{}, len(runnerModes))
	for _, selection := range runnerModes {
		overridden[selection.Name] = struct{}{}
	}
	recorded, err := readRecordedPolicy(scope)
	if err != nil {
		return fmt.Errorf("read the recorded policy: %w", err)
	}
	exclusions, err := newExclusionPlan(
		scope,
		recorded,
		*excludeMode,
		*exclude,
		flagWasSet(flags, "exclude"),
	)
	if err != nil {
		return fmt.Errorf("select skipped directories: %w", err)
	}
	questions, notices, err := initQuestionPlan{
		scope:              scope,
		agentsFlag:         *agents,
		agents:             agentinit.SelectedAgents(selectedAgents),
		catalog:            catalog,
		overriddenRunners:  overridden,
		recorded:           recorded,
		exclusions:         exclusions,
		betaTestAnswered:   flagWasSet(flags, "beta-test"),
		targetAnswered:     instructionsTarget != "",
		aiFamiliesAnswered: *aiFamily != "",
		shellPermission:    parsedShellPermission,
		claudePermissions:  permissions,
		writeMCPConfig:     *writeMCPConfig,
		dryRun:             *dryRun,
	}.questions()
	if err != nil {
		return err
	}
	for _, notice := range notices {
		if writeErr := writeInitOutput(diagnosticOutput, "%s\n", notice); writeErr != nil {
			return writeErr
		}
	}
	renderer, advice := initRenderer(input, diagnosticOutput)
	if advice != "" && len(questions) > 0 {
		if writeErr := writeInitOutput(diagnosticOutput, "%s\n", advice); writeErr != nil {
			return writeErr
		}
	}
	answers, err := renderer.Ask(context.Background(), questions)
	if err != nil {
		return fmt.Errorf("ask init questions: %w", err)
	}
	// The answers were given for the scope resolved before asking, and Apply
	// resolves it again; a scope that moved meanwhile would receive
	// permissions nobody confirmed for it.
	answeredScope, err := agentinit.ResolveScope(*dir)
	if err != nil {
		return fmt.Errorf("resolve init scope: %w", err)
	}
	if answeredScope != scope {
		return fmt.Errorf(
			"the workspace scope changed from %s to %s while init was asking; run init again",
			scope,
			answeredScope,
		)
	}
	if values, asked := answers[betaTestQuestionID]; asked {
		*betaTest = values[0] == questionnaire.Yes
	}
	if values, asked := answers[instructionsTargetQuestionID]; asked {
		instructionsTarget = agentinit.InstructionsTarget(values[0])
	}
	if values, asked := answers[aiFamiliesQuestionID]; asked {
		families, err = aiprofile.ParseSelection(values)
		if err != nil {
			return fmt.Errorf("select AI families %v: %w", values, err)
		}
	}
	canonicalModes, err := answeredRunnerModes(catalog, overrides, answers)
	if err != nil {
		return fmt.Errorf("select runner modes: %w", err)
	}
	excluded, err := exclusions.exclusions(answers)
	if err != nil {
		return fmt.Errorf("select skipped directories: %w", err)
	}
	if values, asked := answers[shellPermissionQuestionID]; asked {
		parsedShellPermission = agentinit.ShellPermission(values[0])
	}
	if values, asked := answers[claudePermissionsQuestionID]; asked {
		permissions = agentinit.ClaudePermissionsNo
		if values[0] == questionnaire.Yes {
			permissions = agentinit.ClaudePermissionsYes
		}
	}
	requestedDirectory, err := filepath.Abs(*dir)
	if err != nil {
		return fmt.Errorf("resolve --dir for instructions target: %w", err)
	}
	targetDirectory, err := agentinit.InstructionsDirectory(
		scope,
		*dir,
		instructionsTarget,
	)
	if err != nil {
		return fmt.Errorf("resolve instructions target directory: %w", err)
	}
	if writeErr := writeInitOutput(
		diagnosticOutput,
		"Agent instructions target %s resolves to directory %s.\n",
		instructionsTarget,
		filepath.Clean(targetDirectory),
	); writeErr != nil {
		return writeErr
	}
	if filepath.Clean(targetDirectory) != filepath.Clean(requestedDirectory) {
		if writeErr := writeInitOutput(
			diagnosticOutput,
			"The instruction files are not in the directory --dir named (%s).\n",
			requestedDirectory,
		); writeErr != nil {
			return writeErr
		}
	}
	result, err := agentinit.Apply(
		agentinit.Options{
			Dir:                 *dir,
			Agents:              selectedAgents,
			BetaTest:            *betaTest,
			PointerInstructions: pointerInstructions,
			DryRun:              *dryRun,
			WriteMCPConfig:      *writeMCPConfig,
			InstructionsTarget:  instructionsTarget,
			AIFamilies:          families,
			RunnerModes:         canonicalModes,
			Exclude:             excluded,
			ClaudePermissions:   permissions,
			ShellPermission:     parsedShellPermission,
		},
	)
	if err != nil {
		return fmt.Errorf("apply agent instructions: %w", err)
	}
	for _, note := range result.Notes {
		if writeErr := writeInitOutput(diagnosticOutput, "%s\n", note); writeErr != nil {
			return writeErr
		}
	}
	return writeInitResult(resultOutput, result, *dryRun, *writeMCPConfig, families)
}

// initRenderer shows the questions as one keyboard-driven form when input and
// the diagnostic output are a terminal that can draw it, and as plain console
// questions everywhere else, the way init has always asked them. The advice
// names a terminal that shows the form when this one cannot.
func initRenderer(input io.Reader, output io.Writer) (questionnaire.Renderer, string) {
	inputFile, inputIsFile := input.(*os.File)
	outputFile, outputIsFile := output.(*os.File)
	if !inputIsFile || !outputIsFile {
		return console.New(input, output), ""
	}
	support := form.Detect(inputFile, outputFile)
	if support.Form {
		return form.New(inputFile, outputFile, "just-mcp-work init"), ""
	}
	return console.New(input, output), support.Advice
}

func resolveInitInstructionsPointer(
	flags *flag.FlagSet,
	dir string,
	requested bool,
) (string, bool, error) {
	scope, err := agentinit.ResolveScope(dir)
	if err != nil {
		return "", false, fmt.Errorf("resolve init scope: %w", err)
	}
	if flagWasSet(flags, "instructions-pointer") {
		return scope, requested, nil
	}
	recorded, known, err := agentinit.ReadRecordedInstructionsPointer(scope)
	if err != nil {
		return "", false, fmt.Errorf("read current instruction-block mode: %w", err)
	}
	if known {
		return scope, recorded, nil
	}
	return scope, requested, nil
}

// flagWasSet reports whether the command line named the flag, so a flag whose
// omission means "ask" or "keep the recorded choice" can tell omission from an
// explicit value equal to its zero value.
func flagWasSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(visited *flag.Flag) {
		if visited.Name == name {
			set = true
		}
	})
	return set
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

// parseInitAIFamilies reads the --ai value, which names as many families as the
// workspace declares, separated the same way the console answer is.
func parseInitAIFamilies(value string) (aiprofile.Selection, error) {
	families, err := aiprofile.ParseSelection(console.SplitAnswerTokens(value))
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

func writeInitOutput(output io.Writer, format string, arguments ...any) error {
	if _, err := fmt.Fprintf(output, format, arguments...); err != nil {
		return fmt.Errorf("write init output: %w", err)
	}
	return nil
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
