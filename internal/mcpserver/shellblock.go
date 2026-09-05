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
}

type defineShellBlockInput struct {
	Command          string `json:"command" jsonschema:"command text interpreted by the operating system shell"`
	WorkingDirectory string `json:"working_directory,omitempty" jsonschema:"workspace-relative directory, default root"`
}

type defineShellBlockOutput struct {
	Error            *toolError `json:"error,omitempty"`
	BlockID          string     `json:"block_id,omitempty"`
	WorkingDirectory string     `json:"working_directory,omitempty"`
}

func defineShellBlockDescription() string {
	return "Define a long ad-hoc shell block once, then run it by block_id from " +
		"run_shell_command or start_shell_command without resending its command text. The command " +
		"and workspace-relative working_directory are fixed when defined. A block lives only for " +
		"this server session; a block_id from an earlier session is an error, not a silent miss."
}

func (s *Server) defineShellBlock(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input defineShellBlockInput,
) (*mcp.CallToolResult, defineShellBlockOutput, error) {
	if strings.TrimSpace(input.Command) == "" {
		err := fmt.Errorf("command must not be empty")
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
) (string, string, error) {
	if command == "" && blockID == "" {
		return "", "", fmt.Errorf("command or block_id is required")
	}
	if command != "" && blockID != "" {
		return "", "", fmt.Errorf(
			"command and block_id must not be combined; use one shell command selector per request",
		)
	}
	if blockID == "" {
		if strings.TrimSpace(command) == "" {
			return "", "", fmt.Errorf("command must not be empty")
		}
		return command, workingDirectory, nil
	}
	if workingDirectory != "" {
		return "", "", fmt.Errorf(
			"working_directory must not be combined with block_id; " +
				"the block already carries its working directory",
		)
	}

	s.shellBlocksMu.Lock()
	block, ok := s.shellBlocks[blockID]
	s.shellBlocksMu.Unlock()
	if !ok {
		return "", "", fmt.Errorf(
			"unknown block_id %q; shell blocks live only for one server session, so define the block again",
			blockID,
		)
	}
	return block.command, block.workingDirectory, nil
}
