// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type shellBlock struct {
	command          string
	workingDirectory string
	argv             []string
}

type defineShellBlockInput struct {
	Command          string   `json:"command,omitempty" jsonschema:"shell text"`
	WorkingDirectory string   `json:"working_directory,omitempty" jsonschema:"workspace directory; default root"`
	Argv             []string `json:"argv,omitempty" jsonschema:"exact argv; no shell"`
}

type defineShellBlockOutput struct {
	Error            *toolError `json:"error,omitempty"`
	BlockID          string     `json:"block_id,omitempty"`
	WorkingDirectory string     `json:"working_directory,omitempty"`
}

func defineShellBlockDescription() string {
	return "Define a reusable long ad-hoc block for run_shell_command or start_shell_command. " +
		"Exactly one of command and argv stores shell text or exact executable arguments; argv " +
		"bypasses the shell; a run repeats it in arguments as the full argv. working_directory is " +
		"fixed. Blocks live only for this server session; a block_id from an earlier session is an " +
		"error, not a silent miss."
}

func (s *Server) defineShellBlock(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input defineShellBlockInput,
) (*mcp.CallToolResult, defineShellBlockOutput, error) {
	hasCommand := input.Command != ""
	// An empty argv carries no executable, so it selects nothing - a client that
	// serialises an absent list as [] must not be read as naming both selectors.
	hasArgv := len(input.Argv) > 0
	if !hasCommand && !hasArgv {
		err := fmt.Errorf("command or argv is required")
		return toolErrorResult(err), defineShellBlockOutput{Error: newToolError(err)}, nil
	}
	if hasCommand && hasArgv {
		err := fmt.Errorf(
			"command and argv must not be combined; use one shell block selector per request",
		)
		return toolErrorResult(err), defineShellBlockOutput{Error: newToolError(err)}, nil
	}
	if hasCommand && strings.TrimSpace(input.Command) == "" {
		err := fmt.Errorf("command must not be empty")
		return toolErrorResult(err), defineShellBlockOutput{Error: newToolError(err)}, nil
	}
	if hasArgv && strings.TrimSpace(input.Argv[0]) == "" {
		err := fmt.Errorf("argv[0] must not be empty")
		return toolErrorResult(err), defineShellBlockOutput{Error: newToolError(err)}, nil
	}
	workingDirectory := canonicalWorkingDirectory(input.WorkingDirectory)
	if _, err := s.workspace.ResolveDir(workingDirectory); err != nil {
		return toolErrorResult(err), defineShellBlockOutput{Error: newToolError(err)}, nil
	}

	s.shellBlocksMu.Lock()
	defer s.shellBlocksMu.Unlock()
	id, err := uuid.NewV7()
	if err != nil {
		err = fmt.Errorf("generate shell block id: %w", err)
		return toolErrorResult(err), defineShellBlockOutput{Error: newToolError(err)}, nil
	}
	blockID := id.String()
	s.shellBlocks[blockID] = shellBlock{
		command:          input.Command,
		argv:             append([]string(nil), input.Argv...),
		workingDirectory: workingDirectory,
	}
	return nil, defineShellBlockOutput{
		BlockID:          blockID,
		WorkingDirectory: workingDirectory,
	}, nil
}

func (s *Server) resolveShellCommand(
	command string,
	blockID string,
	workingDirectory string,
	arguments []string,
) (string, []string, string, error) {
	if err := validateShellCommandSelectors(command, blockID, arguments); err != nil {
		return "", nil, "", err
	}
	if blockID == "" {
		return command, nil, workingDirectory, nil
	}
	if workingDirectory != "" {
		return "", nil, "", fmt.Errorf(
			"working_directory must not be combined with block_id; " +
				"the block already carries its working directory",
		)
	}

	s.shellBlocksMu.Lock()
	block, ok := s.shellBlocks[blockID]
	s.shellBlocksMu.Unlock()
	if !ok {
		return "", nil, "", fmt.Errorf(
			"unknown block_id %q; shell blocks live only for one server session, so define the block again",
			blockID,
		)
	}
	if block.argv == nil {
		if len(arguments) > 0 {
			return "", nil, "", fmt.Errorf(
				"arguments require an argv block; block_id %q names a shell-text block",
				blockID,
			)
		}
		return block.command, nil, block.workingDirectory, nil
	}
	argv, err := resolveArgvBlockRun(blockID, block.argv, arguments)
	if err != nil {
		return "", nil, "", err
	}
	return "", argv, block.workingDirectory, nil
}

// validateShellCommandSelectors rejects a request that names nothing to run,
// names two things to run, or carries arguments with no argv block to extend.
func validateShellCommandSelectors(command, blockID string, arguments []string) error {
	if len(arguments) > 0 && blockID == "" {
		if command != "" {
			return fmt.Errorf(
				"arguments must not be combined with command; arguments require block_id naming an argv block",
			)
		}
		return fmt.Errorf("arguments require block_id naming an argv block")
	}
	if command == "" && blockID == "" {
		return fmt.Errorf("command or block_id is required")
	}
	if command != "" && blockID != "" {
		return fmt.Errorf(
			"command and block_id must not be combined; use one shell command selector per request",
		)
	}
	if blockID == "" && strings.TrimSpace(command) == "" {
		return fmt.Errorf("command must not be empty")
	}
	return nil
}

// resolveArgvBlockRun returns the argv one run executes. A run that adds values
// carries the whole command line, so the call an operator approves names the
// executable it is about to run instead of a block id and a tail of values. The
// block's own argv stays fixed: it is a prefix the run may extend, never edit.
func resolveArgvBlockRun(blockID string, blockArgv, arguments []string) ([]string, error) {
	if len(arguments) == 0 {
		return append([]string(nil), blockArgv...), nil
	}
	if len(arguments) < len(blockArgv) {
		return nil, fmt.Errorf(
			"arguments must be the full argv and start with the %d elements of block_id %q, but carry %d; "+
				"repeat the block's own argv before the added values",
			len(blockArgv),
			blockID,
			len(arguments),
		)
	}
	for index, fixed := range blockArgv {
		if arguments[index] != fixed {
			return nil, fmt.Errorf(
				"arguments[%d] is %q, but block_id %q fixes %q there; "+
					"arguments must be the full argv and start with the block's own argv",
				index,
				arguments[index],
				blockID,
				fixed,
			)
		}
	}
	return append([]string(nil), arguments...), nil
}
