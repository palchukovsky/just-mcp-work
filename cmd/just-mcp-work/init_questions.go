// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/palchukovsky/just-mcp-work/internal/agentinit"
	"github.com/palchukovsky/just-mcp-work/internal/aiprofile"
	"github.com/palchukovsky/just-mcp-work/internal/policy"
	"github.com/palchukovsky/just-mcp-work/internal/questionnaire"
	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

const (
	betaTestQuestionID           = "beta-test"
	instructionsTargetQuestionID = "instructions-target"
	aiFamiliesQuestionID         = "ai-families"
	shellPermissionQuestionID    = "shell-permission"
	claudePermissionsQuestionID  = "claude-permissions"
	runnerQuestionIDPrefix       = "runner:"
	runnersSection               = "Runners"
	permissionsSection           = "Permissions"

	runnerIntro = "A runner offers the tasks of one tool to your AI agents; its mode " +
		"decides which of those commands they can run."
	unreviewedRunnerNote = "JMW has not reviewed this runner's commands yet, so it is " +
		"either fully on or off."
)

// initQuestionPlan holds what init knows before it asks anything: the scope,
// the agents, and every answer a flag already gave. Its questions are exactly
// the decisions no flag answered, in the order init has always asked them.
type initQuestionPlan struct {
	catalog           *runner.Catalog
	overriddenRunners map[string]struct{}
	scope             string
	agentsFlag        string
	shellPermission   agentinit.ShellPermission
	claudePermissions agentinit.ClaudePermissions
	// agents are the agents Apply writes for, as agentinit.SelectedAgents
	// resolves them, so every question matches what Apply will plan.
	agents             []string
	betaTestAnswered   bool
	targetAnswered     bool
	aiFamiliesAnswered bool
	writeMCPConfig     bool
	dryRun             bool
}

// questions returns the questions to ask and the notices no question carries,
// which the caller prints before asking.
func (plan initQuestionPlan) questions() ([]questionnaire.Question, []string, error) {
	questions := make([]questionnaire.Question, 0, len(plan.catalog.PermissionRequests())+5)
	if !plan.betaTestAnswered {
		question, err := betaTestQuestion(plan.scope)
		if err != nil {
			return nil, nil, err
		}
		questions = append(questions, question)
	}
	if !plan.targetAnswered {
		question, err := instructionsTargetQuestion(plan.scope, plan.agentsFlag)
		if err != nil {
			return nil, nil, fmt.Errorf("select instructions target: %w", err)
		}
		questions = append(questions, question)
	}
	if !plan.aiFamiliesAnswered {
		question, err := aiFamiliesQuestion(plan.scope)
		if err != nil {
			return nil, nil, fmt.Errorf("select AI families: %w", err)
		}
		questions = append(questions, question)
	}
	runnerQuestions, notices, err := runnerModeQuestions(
		plan.scope,
		plan.catalog,
		plan.overriddenRunners,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("read current runner modes: %w", err)
	}
	questions = append(questions, runnerQuestions...)
	if plan.asksShellPermission() {
		offer, current, offerErr := agentinit.OfferShellPermission(plan.scope, plan.agents)
		if offerErr != nil {
			return nil, nil, fmt.Errorf("offer shell permission: %w", offerErr)
		}
		questions = append(questions, shellPermissionQuestion(offer, current))
	}
	if plan.asksClaudePermissions() {
		path, pathErr := agentinit.ClaudeSettingsPath(plan.scope)
		if pathErr != nil {
			return nil, nil, fmt.Errorf("resolve Claude settings: %w", pathErr)
		}
		question, questionErr := claudePermissionsQuestion(path, plan.shellPermission)
		if questionErr != nil {
			return nil, nil, questionErr
		}
		questions = append(questions, question)
	}
	return questions, notices, nil
}

// asksShellPermission reports whether Apply needs a shell permission that no
// flag gave.
func (plan initQuestionPlan) asksShellPermission() bool {
	return plan.shellPermission == "" &&
		agentinit.PlansShellPermission(plan.agents, plan.claudePermissions, plan.writeMCPConfig)
}

// asksClaudePermissions reports whether Apply would otherwise have to confirm
// the managed Claude permissions. A dry run plans them as applied and asks
// nothing.
func (plan initQuestionPlan) asksClaudePermissions() bool {
	return !plan.dryRun &&
		plan.claudePermissions == agentinit.ClaudePermissionsAsk &&
		slices.Contains(plan.agents, "claude")
}

func betaTestQuestion(scope string) (questionnaire.Question, error) {
	recorded, found, err := agentinit.ReadRecordedBetaTest(scope)
	var notices []string
	if errors.Is(err, agentinit.ErrUnrecognizedBetaTest) {
		// A mode this binary cannot read is chosen again as in a new
		// workspace; the notice carries the manifest and what is wrong with it.
		notices = []string{fmt.Sprintf(
			"The beta-test mode recorded by an earlier init cannot be used; choose it again: %v",
			err,
		)}
		found, err = false, nil
	}
	if err != nil {
		return questionnaire.Question{}, fmt.Errorf("read current beta-test mode: %w", err)
	}
	offer := questionnaire.No
	if found && recorded {
		offer = questionnaire.Yes
	}
	return questionnaire.Question{
		ID:      betaTestQuestionID,
		Subject: "Beta test",
		Title:   "Should this workspace take part in the JMW beta test?",
		Context: []string{
			"Beta-test mode gives the selected agents and every client connecting to the " +
				"JMW server guidance to report JMW bugs, friction, and missing capabilities.",
		},
		Notices: notices,
		Choices: []questionnaire.Choice{
			{Value: questionnaire.Yes, Description: "take part and add the beta feedback guidance"},
			{Value: questionnaire.No, Description: "plain init, without beta feedback guidance"},
		},
		Defaults: []string{questionnaire.No},
		Offer:    []string{offer},
		Current:  found,
		Label:    "Beta test",
		Flag:     "--beta-test=true|false",
		Parse:    parseYesNo,
		Unsupported: func(value string) error {
			return fmt.Errorf("unsupported beta-test answer %q", value)
		},
		UnsupportedPrompt: func(value string) string {
			return fmt.Sprintf("Unsupported beta-test answer %q; choose one of yes, no.\n", value)
		},
		ReadDescription:       "beta-test answer",
		UnansweredDescription: "beta test",
	}, nil
}

// parseYesNo accepts yes and no, and their first letters, in any case.
func parseYesNo(value string) (string, bool) {
	switch strings.ToLower(value) {
	case "y", questionnaire.Yes:
		return questionnaire.Yes, true
	case "n", questionnaire.No:
		return questionnaire.No, true
	default:
		return "", false
	}
}

func instructionsTargetQuestion(scope string, agentsFlag string) (questionnaire.Question, error) {
	current, found, err := agentinit.ReadRecordedInstructionsTarget(scope)
	if err != nil {
		return questionnaire.Question{}, fmt.Errorf("read current instructions target: %w", err)
	}
	offer := agentinit.InstructionsTargetWorkspace
	if found {
		offer = current
	}
	return questionnaire.Question{
		ID:      instructionsTargetQuestionID,
		Subject: "Instructions target",
		Title:   "Where should the managed agent-instruction block be written?",
		Context: []string{
			"The block is the JMW guidance for your AI agents, kept between markers in " +
				"each selected agent's instruction file, such as CLAUDE.md or AGENTS.md.",
		},
		Choices: []questionnaire.Choice{
			{
				Value:       string(agentinit.InstructionsTargetProject),
				Description: "the instruction files of the directory --dir names",
			},
			{
				Value:       string(agentinit.InstructionsTargetWorkspace),
				Description: "the instruction files at the workspace root",
			},
			{
				Value: string(agentinit.InstructionsTargetMachine),
				Description: "the machine-wide instruction files of Claude Code, Codex, and " +
					"Windsurf, outside this tree",
			},
		},
		Defaults: []string{string(agentinit.InstructionsTargetWorkspace)},
		Offer:    []string{string(offer)},
		Current:  found,
		Label:    "Instructions target",
		Flag:     "--instructions-target project|workspace|machine",
		// A target the selected agents cannot use is refused as soon as it is
		// chosen, before any later question.
		Validate: func(values []string) error {
			return validateInstructionsAgents(agentinit.InstructionsTarget(values[0]), agentsFlag)
		},
		Parse: func(value string) (string, bool) {
			target, parseErr := agentinit.ParseInstructionsTarget(value)
			return string(target), parseErr == nil
		},
		Unsupported: func(value string) error {
			return fmt.Errorf("unsupported instructions target %q", value)
		},
		UnsupportedPrompt: func(value string) string {
			return fmt.Sprintf(
				"Unsupported instructions target %q; choose one of project, workspace, machine.\n",
				value,
			)
		},
		ReadDescription:       "instructions target",
		UnansweredDescription: "instructions target",
	}, nil
}

func aiFamiliesQuestion(scope string) (questionnaire.Question, error) {
	current, found, err := agentinit.ReadRecordedAIFamilies(scope)
	var notices []string
	if errors.Is(err, agentinit.ErrUnrecognizedAIFamilies) {
		// A recorded selection this binary cannot read, such as one written by
		// an older release, is chosen again as in a new workspace. The refusal
		// carries the manifest it came from and what is wrong with it, so the
		// operator sees why the question offers the defaults.
		notices = []string{fmt.Sprintf(
			"The AI families recorded by an earlier init cannot be used; choose them again: %v",
			err,
		)}
		found, err = false, nil
	}
	if err != nil {
		return questionnaire.Question{}, fmt.Errorf("read current AI families: %w", err)
	}
	choices := make([]questionnaire.Choice, 0, len(aiprofile.Declarable()))
	for _, family := range aiprofile.Declarable() {
		config, agent, declared := agentinit.AIFamilyConfig(family)
		if !declared {
			return questionnaire.Question{}, fmt.Errorf(
				"no generated configuration declares AI family %q",
				family,
			)
		}
		choices = append(choices, questionnaire.Choice{
			Value:       string(family),
			Label:       agent,
			Description: "declared in " + config,
		})
	}
	offer := familyNames(defaultAIFamilies())
	if found {
		offer = familyNames(current)
	}
	return questionnaire.Question{
		ID:      aiFamiliesQuestionID,
		Subject: "AI families",
		Title:   "Which AI families should the managed just-mcp-work server declare?",
		Context: []string{
			"Each family is declared in the configuration its own agent reads, so a " +
				"workspace used by both agents declares both.",
			"A family only changes how the server introduces itself to the agent; it " +
				"grants no access.",
			"JMW writes no MCP server configuration for Cursor, GitHub Copilot, or " +
				"Windsurf, so no family applies to them; --agents chooses their " +
				"instruction files.",
		},
		Notices:  notices,
		Choices:  choices,
		Multiple: true,
		Defaults: familyNames(defaultAIFamilies()),
		Offer:    offer,
		Current:  found,
		Label:    "AI families",
		Flag:     "--ai " + initAIFlagValues(),
		Parse: func(value string) (string, bool) {
			if !slices.Contains(aiprofile.Declarable(), aiprofile.Family(value)) {
				return "", false
			}
			return value, true
		},
		Unsupported: func(value string) error {
			return fmt.Errorf("unsupported AI family %q", value)
		},
		UnsupportedPrompt: func(value string) string {
			return fmt.Sprintf(
				"Unsupported AI family %q; choose any of %s.\n",
				value,
				strings.Join(familyNames(aiprofile.Declarable()), ", "),
			)
		},
		ReadDescription:       "AI families",
		UnansweredDescription: "AI families",
	}, nil
}

// runnerModeQuestions asks for the mode of every runner no --runner-mode
// named, offering its current mode when the policy records one. It returns
// the policy notices separately only when no question is left to carry them.
func runnerModeQuestions(
	scope string,
	catalog *runner.Catalog,
	overridden map[string]struct{},
) ([]questionnaire.Question, []string, error) {
	currentModes, notices, err := currentRunnerModes(scope, catalog)
	if err != nil {
		return nil, nil, err
	}
	var questions []questionnaire.Question
	for _, request := range catalog.PermissionRequests() {
		if _, found := overridden[request.Name]; found {
			continue
		}
		offer, current := request.Default, false
		if currentMode, found := currentModes[request.Name]; found {
			offer, current = currentMode, true
		}
		question := runnerModeQuestion(request, offer, current)
		if len(questions) == 0 {
			// The notices explain the offers, so they come before the first one.
			question.Notices = notices
		}
		questions = append(questions, question)
	}
	if len(questions) == 0 {
		// Flags answered every runner, so no offer needs the notices; they
		// still report what happened to the recorded policy.
		return nil, notices, nil
	}
	return questions, nil, nil
}

// currentRunnerModes reads the modes the policy records for the registered
// runners. A policy that cannot be parsed, or that names an unsupported mode,
// is replaced by the declared defaults and reported by a notice; a policy that
// cannot be read at all is an error.
func currentRunnerModes(
	scope string,
	catalog *runner.Catalog,
) (map[string]runner.Mode, []string, error) {
	data, found, err := policy.Read(scope)
	if err != nil {
		return nil, nil, fmt.Errorf("read runner policy %s: %w", policy.Path(scope), err)
	}
	if !found {
		return map[string]runner.Mode{}, nil, nil
	}
	currentPolicy, err := policy.Parse(data)
	if err != nil {
		return map[string]runner.Mode{}, []string{policyFallbackNotice(scope, err)}, nil
	}
	current, changed, err := currentModesForCatalog(catalog, currentPolicy.Selections)
	if err != nil {
		return map[string]runner.Mode{}, []string{policyFallbackNotice(scope, err)}, nil
	}
	if !changed {
		return current, nil, nil
	}
	return current, []string{
		"Registered runner set changed; keeping current modes for matching runners, " +
			"using declared defaults for new runners, and dropping unregistered runners.",
	}, nil
}

func policyFallbackNotice(scope string, policyErr error) string {
	return fmt.Sprintf(
		"Existing runner policy %s could not be read; using declared defaults: %v",
		policy.Path(scope),
		policyErr,
	)
}

func runnerModeQuestion(
	request runner.PermissionRequest,
	offer runner.Mode,
	current bool,
) questionnaire.Question {
	context := []string{request.Summary, runnerIntro}
	if !request.Reviewed {
		context = append(context, unreviewedRunnerNote)
	}
	choices := make([]questionnaire.Choice, 0, len(request.Choices))
	for _, choice := range request.Choices {
		choices = append(choices, questionnaire.Choice{
			Value:       string(choice.Mode),
			Label:       choice.Label,
			Description: choice.Description,
			Warning:     choice.Warning,
		})
	}
	return questionnaire.Question{
		ID:       runnerQuestionIDPrefix + request.Name,
		Section:  runnersSection,
		Subject:  request.Title,
		Title:    fmt.Sprintf("Which mode should the %s runner use?", request.Title),
		Context:  context,
		Choices:  choices,
		Defaults: []string{string(request.Default)},
		Offer:    []string{string(offer)},
		Current:  current,
		Label:    "Mode",
		Flag:     fmt.Sprintf("--runner-mode %s=<mode>", request.Name),
		Parse: func(value string) (string, bool) {
			mode, found := findRequestedMode(request, value)
			return string(mode), found
		},
		Unsupported: func(value string) error {
			return fmt.Errorf("unsupported mode %q for runner %q", value, request.Name)
		},
		UnsupportedPrompt: func(value string) string {
			return fmt.Sprintf(
				"Unsupported mode %q; choose one of %s.\n",
				value,
				requestModeNames(request),
			)
		},
		ReadDescription:       request.Name + " runner mode",
		UnansweredDescription: fmt.Sprintf("runner %q mode", request.Name),
	}
}

func shellPermissionQuestion(
	offer agentinit.ShellPermission,
	current bool,
) questionnaire.Question {
	return questionnaire.Question{
		ID:      shellPermissionQuestionID,
		Section: permissionsSection,
		Subject: "Shell commands",
		Title:   "May your AI agents run shell commands through JMW without asking you?",
		Context: []string{
			"run_shell_command and start_shell_command run any command line with your " +
				"user's permissions, and define_shell_block prepares one for them.",
			"The answer sets how Claude Code's permission lists and Codex's approval " +
				"modes treat these tools, wherever JMW manages them.",
		},
		Choices: []questionnaire.Choice{
			{
				Value:       string(agentinit.ShellPermissionAllow),
				Label:       "Run without asking",
				Description: "the Claude Code allow list and the Codex approve mode",
			},
			{
				Value:       string(agentinit.ShellPermissionAsk),
				Label:       "Ask every time",
				Description: "the Claude Code ask list and the Codex prompt mode",
			},
		},
		Defaults: []string{string(agentinit.ShellPermissionAsk)},
		Offer:    []string{string(offer)},
		Current:  current,
		Label:    "Shell commands",
		Flag:     "--shell-permission allow|ask",
		Parse: func(value string) (string, bool) {
			permission, err := agentinit.ParseShellPermission(value)
			return string(permission), err == nil
		},
		Unsupported: func(value string) error {
			return fmt.Errorf("unsupported shell permission %q", value)
		},
		UnsupportedPrompt: func(value string) string {
			return fmt.Sprintf(
				"Unsupported shell permission %q; choose one of allow, ask.\n",
				value,
			)
		},
		ReadDescription:       "shell permission",
		UnansweredDescription: "shell permission",
	}
}

// claudePermissionsQuestion asks whether to write the JMW tool permissions
// into the Claude Code settings at path. Which tools it says would still ask
// follows the shell permission, which is always settled first: by its flag, or
// by the shell question, which comes before this one.
func claudePermissionsQuestion(
	path string,
	shellFlag agentinit.ShellPermission,
) (questionnaire.Question, error) {
	lists := make(map[agentinit.ShellPermission][]string, 2)
	for _, permission := range []agentinit.ShellPermission{
		agentinit.ShellPermissionAllow,
		agentinit.ShellPermissionAsk,
	} {
		managed, err := agentinit.ClaudeManagedTools(permission)
		if err != nil {
			return questionnaire.Question{}, fmt.Errorf("resolve managed Claude tools: %w", err)
		}
		lines := []string{"With yes, these run without asking: " + claudeToolNames(managed.Allow)}
		if len(managed.Ask) > 0 {
			lines = append(lines, "With yes, these still ask every time: "+claudeToolNames(managed.Ask))
		}
		lists[permission] = lines
	}
	return questionnaire.Question{
		ID:      claudePermissionsQuestionID,
		Kind:    questionnaire.Confirm,
		Section: permissionsSection,
		Subject: "Claude Code permissions",
		Title:   "Should JMW allow its tools in the Claude Code settings?",
		ContextFor: func(answers questionnaire.Answers) []string {
			permission := shellFlag
			if values, asked := answers[shellPermissionQuestionID]; asked {
				permission = agentinit.ShellPermission(values[0])
			}
			return slices.Concat([]string{
				"Claude Code asks you before it uses a tool its settings do not allow. " +
					"JMW can allow its own tools in " + path + ".",
			}, lists[permission])
		},
		Choices: []questionnaire.Choice{
			{
				Value:       questionnaire.Yes,
				Label:       "Allow JMW tools",
				Description: "write the JMW entries into the settings, replacing earlier ones",
			},
			{
				Value: questionnaire.No,
				Label: "Ask every time",
				Description: "remove the JMW entries from this file, and the file too if " +
					"nothing else is left in it; Claude Code then asks before each JMW " +
					"tool, unless another of its settings files allows it",
			},
		},
		Defaults: []string{questionnaire.No},
		Offer:    []string{questionnaire.No},
		Label:    "Allow them?",
		Flag:     "--claude-permissions yes|no",
		UnansweredNote: "No answer, so the JMW entries are not written, and any existing ones " +
			"are removed, with the file itself if nothing else is left in it. " +
			"Use --claude-permissions=yes to write them, or --claude-permissions=no " +
			"to skip this question and remove them.",
		ReadDescription: "claude permissions confirmation for " + path,
	}, nil
}

// claudeToolNames lists Claude permission rules by the JMW tool names they
// allow, without the prefix every rule shares.
func claudeToolNames(rules []string) string {
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, strings.TrimPrefix(rule, agentinit.ClaudeToolPrefix))
	}
	return strings.Join(names, ", ")
}

// answeredRunnerModes completes the canonical --runner-mode selection with
// the modes the questionnaire chose.
func answeredRunnerModes(
	catalog *runner.Catalog,
	overrides runner.ValidatedSelections,
	answers questionnaire.Answers,
) (runner.ValidatedSelections, error) {
	selections, err := overrides.Selections()
	if err != nil {
		return runner.ValidatedSelections{}, fmt.Errorf("read canonical runner modes: %w", err)
	}
	for index, selection := range selections {
		if values, asked := answers[runnerQuestionIDPrefix+selection.Name]; asked {
			selections[index].Mode = runner.Mode(values[0])
		}
	}
	canonical, err := catalog.CanonicalSelections(selections)
	if err != nil {
		return runner.ValidatedSelections{}, fmt.Errorf(
			"canonicalize selected runner modes: %w",
			err,
		)
	}
	return canonical, nil
}
