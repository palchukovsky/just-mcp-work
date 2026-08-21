// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
)

const (
	maxGitMarkerBytes            = 4096
	maxGitConfigBytes            = 1 << 20
	outsideWorkspaceMainCheckout = "<outside-workspace>"
	bareRepositoryMainCheckout   = "<bare-repository>"
)

// Worktree identifies a project as a linked Git worktree.
type Worktree struct {
	// MainCheckout is the workspace-relative path of the main checkout.
	MainCheckout string `json:"main_checkout"`
}

type commonGitRepository struct {
	commonDir    string
	worktree     string
	bare         bool
	conventional bool
}

type redactedResolverError struct {
	cause   error
	message string
}

func (e redactedResolverError) Error() string { return e.message }

func (e redactedResolverError) Unwrap() error { return e.cause }

type gitCoreConfig struct {
	worktree          string
	bare              bool
	worktreeConfig    bool
	hasWorktree       bool
	hasBare           bool
	hasWorktreeConfig bool
}

type resolvedPathKind uint8

const (
	resolvedDirectory resolvedPathKind = iota
	resolvedRegularFile
)

type pathState uint8

const (
	pathInvalid pathState = iota
	pathResolved
	pathSymlink
	pathAmbiguous
	pathOutside
)

type inspectedEntry struct {
	err   error
	mode  fs.FileMode
	state pathState
}

type directoryListing struct {
	exact     map[string]fs.DirEntry
	folded    map[string][]fs.DirEntry
	inspected map[string]inspectedEntry
	err       error
	missing   bool
}

type pathResolver struct {
	ctx      context.Context
	listings map[string]*directoryListing
}

func (r *Registry) registeredWorktreeDirs(
	ctx context.Context,
	mainDir string,
	scanBase string,
	resolver *pathResolver,
) ([]string, error) {
	worktreesDir, entries, registered, err := r.worktreeRegistry(mainDir, resolver)
	if err != nil {
		return nil, err
	}
	if !registered {
		return nil, nil
	}
	resolvedScanBase, state, err := resolver.resolveWithin(
		r.root,
		scanBase,
		resolvedDirectory,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve scan base %q: %w", scanBase, err)
	}
	if state != pathResolved {
		return nil, fmt.Errorf("scan base %q is not a plain directory", scanBase)
	}
	dirs := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("discover git worktrees for %q: %w", mainDir, err)
		}
		candidate, accepted, entryErr := r.registeredWorktreeDir(
			worktreesDir,
			entry,
			resolvedScanBase,
			resolver,
		)
		if entryErr != nil {
			return nil, entryErr
		}
		if !accepted {
			continue
		}
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}
		dirs = append(dirs, candidate)
	}
	return dirs, nil
}

func (r *Registry) worktreeRegistry(
	mainDir string,
	resolver *pathResolver,
) (string, []fs.DirEntry, bool, error) {
	gitDir := filepath.Join(mainDir, ".git")
	plain, err := isPlainDir(gitDir)
	if err != nil {
		return "", nil, false, fmt.Errorf("inspect git directory for %q: %w", mainDir, err)
	}
	if !plain {
		return "", nil, false, nil
	}
	worktreesDir := filepath.Join(gitDir, "worktrees")
	plain, err = isPlainDir(worktreesDir)
	if err != nil {
		return "", nil, false, fmt.Errorf(
			"inspect git worktree registry for %q: %w",
			mainDir,
			err,
		)
	}
	if !plain {
		return "", nil, false, nil
	}
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return "", nil, false, fmt.Errorf("read git worktree registry for %q: %w", mainDir, err)
	}
	if seedErr := resolver.seed(worktreesDir, entries); seedErr != nil {
		return "", nil, false, fmt.Errorf("index git worktree registry for %q: %w", mainDir, seedErr)
	}
	return worktreesDir, entries, true, nil
}

func (r *Registry) registeredWorktreeDir(
	worktreesDir string,
	entry fs.DirEntry,
	scanBase string,
	resolver *pathResolver,
) (string, bool, error) {
	entryDir := filepath.Join(worktreesDir, entry.Name())
	candidateGitFile, candidate, err := registeredWorktreeCandidate(entryDir)
	if err != nil {
		return "", false, fmt.Errorf("inspect git worktree entry %q: %w", entryDir, err)
	}
	if !candidate {
		return "", false, nil
	}
	worktreeDir, marker, allowed, err := r.worktreePathAllowed(
		resolver,
		candidateGitFile,
		scanBase,
	)
	if err != nil {
		return "", false, fmt.Errorf("validate git worktree path %q: %w", candidateGitFile, err)
	}
	if !allowed {
		return "", false, nil
	}
	backReference, linked, err := linkedWorktreeGitDir(marker)
	if err != nil {
		return "", false, fmt.Errorf("inspect git worktree %q: %w", worktreeDir, err)
	}
	if !linked {
		return "", false, nil
	}
	resolvedEntry, state, err := resolver.resolveWithin(
		worktreesDir,
		backReference,
		resolvedDirectory,
	)
	if err != nil {
		return "", false, fmt.Errorf("validate git worktree entry %q: %w", entryDir, err)
	}
	if state != pathResolved || resolvedEntry != filepath.Clean(entryDir) {
		return "", false, nil
	}
	return worktreeDir, true, nil
}

func registeredWorktreeCandidate(entryDir string) (string, bool, error) {
	plain, err := isPlainDir(entryDir)
	if err != nil {
		return "", false, err
	}
	if !plain {
		return "", false, nil
	}
	gitFile, ok, err := readMarker(filepath.Join(entryDir, "gitdir"))
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	gitFile, ok = markerLine(gitFile)
	if !ok {
		return "", false, nil
	}
	gitFile = resolveMarkerPath(filepath.Join(entryDir, "gitdir"), gitFile)
	if !pathNamesEqual(filepath.Base(gitFile), ".git") {
		return "", false, nil
	}
	return filepath.Clean(gitFile), true, nil
}

func (r *Registry) worktreePathAllowed(
	resolver *pathResolver,
	candidateGitFile string,
	scanBase string,
) (string, string, bool, error) {
	resolvedGitFile, state, err := resolver.resolveWithin(
		r.root,
		candidateGitFile,
		resolvedRegularFile,
	)
	if err != nil {
		return "", "", false, err
	}
	if state != pathResolved {
		return "", "", false, nil
	}
	canonical, canonicalErr := resolver.isCanonicalGitMarker(resolvedGitFile)
	if canonicalErr != nil {
		return "", "", false, canonicalErr
	}
	if !canonical {
		return "", "", false, nil
	}
	candidate := filepath.Dir(resolvedGitFile)
	if !canonicalPathWithin(r.root, candidate) ||
		!canonicalPathWithin(scanBase, candidate) ||
		r.scanBaseExcluded(candidate) {
		return "", "", false, nil
	}
	return candidate, resolvedGitFile, true, nil
}

// linkedWorktree determines whether dir is a registered linked worktree and
// which main checkout should be reported. It validates the marker and registry
// binding, then reads core.worktree and core.bare to classify the main checkout.
//
//nolint:gocyclo // Each branch rejects an ambiguous or unsafe repository identity.
func (r *Registry) linkedWorktree(
	dir string,
	resolver *pathResolver,
) (Worktree, bool, error) {
	gitDir, linked, err := linkedWorktreeGitDir(filepath.Join(dir, ".git"))
	if err != nil {
		return Worktree{}, false, err
	}
	if !linked {
		return Worktree{}, false, nil
	}
	resolvedDir, state, err := resolver.resolveWithin(r.root, dir, resolvedDirectory)
	if err != nil {
		return Worktree{}, false, fmt.Errorf("resolve worktree directory %q: %w", dir, err)
	}
	if state != pathResolved {
		return Worktree{}, false, nil
	}
	commonDir, valid := commonGitDir(gitDir)
	if !valid {
		return Worktree{}, false, nil
	}
	resolvedGitDir, registered, err := r.registeredCommonWorktree(
		resolvedDir,
		commonDir,
		gitDir,
		resolver,
	)
	if err != nil {
		return Worktree{}, false, fmt.Errorf("validate common git worktree registry: %w", err)
	}
	if !registered {
		return Worktree{}, false, nil
	}
	resolvedCommonDir, valid := commonGitDir(resolvedGitDir)
	if !valid {
		return Worktree{}, false, nil
	}
	containedCommonDir, commonState, err := resolver.resolveWithin(
		r.root,
		resolvedCommonDir,
		resolvedDirectory,
	)
	if err != nil {
		return Worktree{}, false, fmt.Errorf("contain registered common git directory: %w", err)
	}
	if commonState == pathOutside {
		return Worktree{MainCheckout: outsideWorkspaceMainCheckout}, true, nil
	}
	if commonState != pathResolved {
		return Worktree{}, false, fmt.Errorf("registered common git directory is unsafe or missing")
	}
	repository, err := inspectCommonGitRepository(containedCommonDir)
	if err != nil {
		return Worktree{}, false, fmt.Errorf(
			"classify common git directory %q: %w",
			containedCommonDir,
			err,
		)
	}
	if repository.bare {
		return Worktree{MainCheckout: bareRepositoryMainCheckout}, true, nil
	}
	mainCheckout, state, err := resolver.resolveWithin(
		r.root,
		repository.worktree,
		resolvedDirectory,
	)
	if err != nil {
		return Worktree{}, false, redactedResolverError{
			message: "validate configured main checkout",
			cause:   err,
		}
	}
	if state == pathOutside || state == pathSymlink {
		return Worktree{MainCheckout: outsideWorkspaceMainCheckout}, true, nil
	}
	if state != pathResolved {
		return Worktree{}, false, fmt.Errorf("configured main checkout is unsafe or missing")
	}
	if repository.conventional {
		resolvedCommon, commonState, resolveErr := resolver.resolveExactChild(
			mainCheckout,
			".git",
			resolvedDirectory,
		)
		if resolveErr != nil {
			return Worktree{}, false, fmt.Errorf("validate conventional git directory: %w", resolveErr)
		}
		if commonState != pathResolved || resolvedCommon != repository.commonDir {
			return Worktree{}, false, fmt.Errorf(
				"conventional common git directory %q does not match main checkout %q",
				repository.commonDir,
				mainCheckout,
			)
		}
	}
	rel, err := filepath.Rel(r.root, mainCheckout)
	if err != nil {
		return Worktree{}, false, redactedResolverError{
			message: "resolve configured main checkout relative to workspace",
			cause:   err,
		}
	}
	return Worktree{MainCheckout: filepath.ToSlash(rel)}, true, nil
}

func (r *Registry) registeredCommonWorktree(
	dir string,
	commonDir string,
	gitDir string,
	resolver *pathResolver,
) (string, bool, error) {
	resolvedGitDir, state, err := resolver.resolveCommonGitRegistryEntry(commonDir, gitDir)
	if err != nil {
		return "", false, err
	}
	if state != pathResolved {
		return "", false, nil
	}
	registered, err := r.registeredResolvedWorktree(dir, resolvedGitDir, resolver)
	if err != nil || !registered {
		return "", registered, err
	}
	return resolvedGitDir, true, nil
}

func (r *Registry) registeredResolvedWorktree(
	dir string,
	gitDir string,
	resolver *pathResolver,
) (bool, error) {
	candidateGitFile, registered, err := registeredWorktreeCandidate(gitDir)
	if err != nil {
		return false, fmt.Errorf("inspect git worktree entry %q: %w", gitDir, err)
	}
	if !registered {
		return false, nil
	}
	resolvedGitFile, state, err := resolver.resolveWithin(
		r.root,
		candidateGitFile,
		resolvedRegularFile,
	)
	if err != nil {
		return false, fmt.Errorf("validate git worktree path %q: %w", dir, err)
	}
	if state != pathResolved {
		return false, nil
	}
	canonical, err := resolver.isCanonicalGitMarker(resolvedGitFile)
	if err != nil {
		return false, fmt.Errorf("validate git worktree path %q: %w", dir, err)
	}
	if !canonical || filepath.Dir(resolvedGitFile) != dir {
		return false, nil
	}
	return true, nil
}

func linkedWorktreeGitDir(markerPath string) (string, bool, error) {
	marker, ok, err := readMarker(markerPath)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	line, ok := markerLine(marker)
	if !ok {
		return "", false, nil
	}
	gitDir, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok || gitDir == "" {
		return "", false, nil
	}
	gitDir = resolveMarkerPath(markerPath, gitDir)
	if _, ok := commonGitDir(gitDir); !ok {
		return "", false, nil
	}
	return gitDir, true, nil
}

// ActiveWorktreeRoot returns the canonical linked-worktree root containing dir.
// The upward walk stops at the first repository boundary. A structurally linked
// marker is authoritative: malformed metadata or a failed registry round trip is
// an error instead of permission to use an ancestor scope.
func ActiveWorktreeRoot(dir string) (string, bool, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false, fmt.Errorf("resolve active directory: %w", err)
	}
	canonicalDir, err := canonicalPathWithMissing(absDir)
	if err != nil {
		return "", false, err
	}
	// The terminal condition must stay in the advance path below. Hoisting it
	// to the top of the body would return before the marker of a volume or UNC
	// root is ever inspected, silently reporting a linked worktree there as
	// unlinked. No unprivileged test can write to such a root, so this comment
	// is the only guard the invariant has.
	for current := canonicalDir; ; {
		markerPath := filepath.Join(current, ".git")
		info, inspectErr := os.Lstat(markerPath)
		if inspectErr == nil {
			if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
				return "", false, nil
			}
			if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return "", false, fmt.Errorf(
					"active git marker %q is not a regular file or directory",
					markerPath,
				)
			}
			gitDir, linked, markerErr := requiredLinkedWorktreeGitDir(markerPath)
			if markerErr != nil {
				return "", false, markerErr
			}
			if !linked {
				return "", false, nil
			}
			if validateErr := validateActiveWorktree(current, markerPath, gitDir); validateErr != nil {
				return "", false, validateErr
			}
			return current, true, nil
		}
		if !errors.Is(inspectErr, fs.ErrNotExist) {
			return "", false, fmt.Errorf("inspect active git marker %q: %w", markerPath, inspectErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false, nil
		}
		current = parent
	}
}

func requiredLinkedWorktreeGitDir(markerPath string) (string, bool, error) {
	marker, err := readRequiredMarker(markerPath)
	if err != nil {
		return "", false, err
	}
	line, ok := markerLine(marker)
	if !ok {
		return "", false, fmt.Errorf("active git marker %q must contain one line", markerPath)
	}
	gitDir, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok || gitDir == "" {
		return "", false, fmt.Errorf("active git marker %q has invalid gitdir syntax", markerPath)
	}
	gitDir = resolveMarkerPath(markerPath, gitDir)
	if _, linked := commonGitDir(gitDir); !linked {
		return "", false, nil
	}
	return gitDir, true, nil
}

//nolint:gocyclo // Repository and config filesystem states require explicit classification.
func inspectCommonGitRepository(
	commonDir string,
) (commonGitRepository, error) {
	cleanCommonDir := filepath.Clean(commonDir)
	canonicalCommonDir, err := filepath.EvalSymlinks(cleanCommonDir)
	if err != nil {
		return commonGitRepository{}, fmt.Errorf("resolve common git directory: %w", err)
	}
	if canonicalCommonDir != cleanCommonDir {
		return commonGitRepository{}, fmt.Errorf(
			"common git directory %q contains a symlink",
			cleanCommonDir,
		)
	}
	info, err := os.Lstat(canonicalCommonDir)
	if err != nil {
		return commonGitRepository{}, fmt.Errorf("inspect common git directory: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return commonGitRepository{}, fmt.Errorf(
			"common git directory %q is not a non-symlink directory",
			canonicalCommonDir,
		)
	}
	config, err := readGitCoreConfig(filepath.Join(canonicalCommonDir, "config"))
	if err != nil {
		return commonGitRepository{}, err
	}
	if config.worktreeConfig {
		configWorktreePath := filepath.Join(canonicalCommonDir, "config.worktree")
		configWorktreeInfo, configWorktreeErr := os.Lstat(configWorktreePath)
		switch {
		case errors.Is(configWorktreeErr, fs.ErrNotExist):
		case configWorktreeErr != nil:
			return commonGitRepository{}, fmt.Errorf(
				"inspect per-worktree git config %q: %w",
				configWorktreePath,
				configWorktreeErr,
			)
		case configWorktreeInfo.Mode()&fs.ModeSymlink != 0 ||
			!configWorktreeInfo.Mode().IsRegular():
			return commonGitRepository{}, fmt.Errorf(
				"per-worktree git config %q is unsafe",
				configWorktreePath,
			)
		default:
			return commonGitRepository{}, fmt.Errorf(
				"per-worktree configuration %q is present and unsupported",
				configWorktreePath,
			)
		}
	}
	if !config.hasBare {
		return commonGitRepository{}, fmt.Errorf("git config is missing required core.bare")
	}
	if config.bare {
		if config.hasWorktree {
			return commonGitRepository{}, fmt.Errorf(
				"git config has conflicting core.bare=true and core.worktree",
			)
		}
		return commonGitRepository{commonDir: canonicalCommonDir, bare: true}, nil
	}
	if config.hasWorktree {
		worktree := config.worktree
		if !filepath.IsAbs(worktree) {
			worktree = filepath.Join(canonicalCommonDir, worktree)
		}
		return commonGitRepository{
			commonDir: canonicalCommonDir,
			worktree:  filepath.Clean(worktree),
		}, nil
	}
	if !pathNamesEqual(filepath.Base(canonicalCommonDir), ".git") {
		return commonGitRepository{}, fmt.Errorf(
			"non-bare custom git directory %q is missing required core.worktree",
			canonicalCommonDir,
		)
	}
	return commonGitRepository{
		commonDir:    canonicalCommonDir,
		worktree:     filepath.Dir(canonicalCommonDir),
		conventional: true,
	}, nil
}

func readGitCoreConfig(path string) (config gitCoreConfig, err error) {
	lstatInfo, err := os.Lstat(path)
	if err != nil {
		return gitCoreConfig{}, fmt.Errorf("inspect git config %q: %w", path, err)
	}
	if lstatInfo.Mode()&fs.ModeSymlink != 0 || !lstatInfo.Mode().IsRegular() {
		return gitCoreConfig{}, fmt.Errorf("git config %q is not a regular non-symlink file", path)
	}
	// #nosec G304 -- path is the fixed config basename under a validated common Git directory.
	file, err := os.Open(path)
	if err != nil {
		return gitCoreConfig{}, fmt.Errorf("open git config %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close git config %q: %w", path, closeErr)
		}
	}()
	// os.Open may follow a swapped final component; descriptor identity detects it before reads.
	// Blocking replacements such as FIFOs and the parent-directory race are tracked separately.
	statInfo, err := file.Stat()
	if err != nil {
		return gitCoreConfig{}, fmt.Errorf("inspect opened git config %q: %w", path, err)
	}
	if !os.SameFile(lstatInfo, statInfo) {
		return gitCoreConfig{}, fmt.Errorf("git config %q changed during open", path)
	}
	if statInfo.Size() > maxGitConfigBytes {
		return gitCoreConfig{}, fmt.Errorf("git config %q exceeds %d bytes", path, maxGitConfigBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxGitConfigBytes+1))
	if err != nil {
		return gitCoreConfig{}, fmt.Errorf("read git config %q: %w", path, err)
	}
	if len(content) > maxGitConfigBytes {
		return gitCoreConfig{}, fmt.Errorf("git config %q exceeds %d bytes", path, maxGitConfigBytes)
	}
	config, err = parseGitCoreConfig(string(content))
	if err != nil {
		return gitCoreConfig{}, fmt.Errorf("parse git config %q: %w", path, err)
	}
	return config, nil
}

//nolint:gocyclo // Every rejected syntax branch prevents ambiguous repository classification.
func parseGitCoreConfig(content string) (gitCoreConfig, error) {
	var config gitCoreConfig
	section := ""
	for lineNumber, rawLine := range strings.Split(content, "\n") {
		line, err := gitConfigLine(rawLine)
		if err != nil {
			return gitCoreConfig{}, fmt.Errorf("line %d: %w", lineNumber+1, err)
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			sectionName, sectionErr := gitConfigSection(line)
			if sectionErr != nil {
				return gitCoreConfig{}, fmt.Errorf("line %d: %w", lineNumber+1, sectionErr)
			}
			if sectionName == "include" || sectionName == "includeif" {
				return gitCoreConfig{}, fmt.Errorf("line %d: include-driven config is unsupported", lineNumber+1)
			}
			section = sectionName
			continue
		}
		if section == "" {
			return gitCoreConfig{}, fmt.Errorf(
				"line %d: assignment outside a section is unsupported",
				lineNumber+1,
			)
		}
		key, value, hasValue, err := gitConfigAssignment(line)
		if err != nil {
			return gitCoreConfig{}, fmt.Errorf("line %d: %w", lineNumber+1, err)
		}
		switch section {
		case "core":
			switch strings.ToLower(key) {
			case "bare":
				if config.hasBare {
					return gitCoreConfig{}, fmt.Errorf(
						"line %d: duplicate core.bare",
						lineNumber+1,
					)
				}
				bare, booleanErr := gitConfigBoolean(value, hasValue)
				if booleanErr != nil {
					return gitCoreConfig{}, fmt.Errorf(
						"line %d: invalid core.bare: %w",
						lineNumber+1,
						booleanErr,
					)
				}
				config.bare = bare
				config.hasBare = true
			case "worktree":
				if config.hasWorktree {
					return gitCoreConfig{}, fmt.Errorf(
						"line %d: duplicate core.worktree",
						lineNumber+1,
					)
				}
				if !hasValue || value == "" {
					return gitCoreConfig{}, fmt.Errorf(
						"line %d: core.worktree requires a value",
						lineNumber+1,
					)
				}
				config.worktree = value
				config.hasWorktree = true
			}
		case "extensions":
			if !strings.EqualFold(key, "worktreeconfig") {
				continue
			}
			if config.hasWorktreeConfig {
				return gitCoreConfig{}, fmt.Errorf(
					"line %d: duplicate extensions.worktreeConfig",
					lineNumber+1,
				)
			}
			worktreeConfig, booleanErr := gitConfigBoolean(value, hasValue)
			if booleanErr != nil {
				return gitCoreConfig{}, fmt.Errorf(
					"line %d: invalid extensions.worktreeConfig: %w",
					lineNumber+1,
					booleanErr,
				)
			}
			config.worktreeConfig = worktreeConfig
			config.hasWorktreeConfig = true
		}
	}
	if config.hasBare && config.bare && config.hasWorktree {
		return gitCoreConfig{}, fmt.Errorf("conflicting core.bare=true and core.worktree")
	}
	return config, nil
}

func gitConfigLine(line string) (string, error) {
	line = strings.TrimSuffix(line, "\r")
	inQuote := false
	escaped := false
	for index := range len(line) {
		char := line[index]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && (char == '#' || char == ';') {
			return strings.TrimSpace(line[:index]), nil
		}
	}
	if escaped {
		return "", fmt.Errorf("line continuation is unsupported")
	}
	if inQuote {
		return "", fmt.Errorf("unterminated quote")
	}
	return strings.TrimSpace(line), nil
}

func gitConfigSection(line string) (string, error) {
	if !strings.HasSuffix(line, "]") {
		return "", fmt.Errorf("invalid section header")
	}
	header := strings.TrimSpace(line[1 : len(line)-1])
	if header == "" {
		return "", fmt.Errorf("empty section header")
	}
	end := strings.IndexAny(header, " \t\"")
	if end < 0 {
		end = len(header)
	}
	name := header[:end]
	if !validGitConfigName(name) {
		return "", fmt.Errorf("invalid section name")
	}
	lowerName := strings.ToLower(name)
	remainder := strings.TrimSpace(header[end:])
	if remainder != "" &&
		(len(remainder) < 2 || remainder[0] != '"' || remainder[len(remainder)-1] != '"') {
		return "", fmt.Errorf("invalid section subsection")
	}
	if remainder != "" {
		if lowerName == "include" || lowerName == "includeif" {
			return lowerName, nil
		}
		return lowerName + ".subsection", nil
	}
	return lowerName, nil
}

func gitConfigAssignment(line string) (string, string, bool, error) {
	key, rawValue, hasValue := strings.Cut(line, "=")
	if hasValue {
		key = strings.TrimSpace(key)
		rawValue = strings.TrimSpace(rawValue)
	}
	key = strings.TrimSpace(key)
	if !validGitConfigName(key) {
		return "", "", false, fmt.Errorf("invalid variable name")
	}
	if !hasValue {
		return key, "", false, nil
	}
	value, err := gitConfigValue(rawValue)
	if err != nil {
		return "", "", false, err
	}
	return key, value, true, nil
}

func validGitConfigName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range []byte(name) {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
			index > 0 && (char >= '0' && char <= '9' || char == '-') {
			continue
		}
		return false
	}
	return true
}

func gitConfigValue(value string) (string, error) {
	quoted := strings.HasPrefix(value, "\"")
	if quoted {
		if len(value) < 2 || !strings.HasSuffix(value, "\"") {
			return "", fmt.Errorf("unterminated quoted value")
		}
		value = value[1 : len(value)-1]
	} else if strings.Contains(value, "\"") {
		return "", fmt.Errorf("unsupported quote placement")
	}
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char != '\\' {
			result.WriteByte(char)
			continue
		}
		index++
		if index == len(value) {
			return "", fmt.Errorf("line continuation is unsupported")
		}
		switch value[index] {
		case 'n':
			result.WriteByte('\n')
		case 't':
			result.WriteByte('\t')
		case 'b':
			result.WriteByte('\b')
		case '\\', '"':
			result.WriteByte(value[index])
		default:
			return "", fmt.Errorf("unsupported escape sequence")
		}
	}
	return result.String(), nil
}

func gitConfigBoolean(value string, hasValue bool) (bool, error) {
	if !hasValue {
		return true, nil
	}
	switch strings.ToLower(value) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	default:
		return false, fmt.Errorf("unsupported boolean value")
	}
}

// validateActiveWorktree validates that worktreeRoot is the linked worktree
// containing JMW. It checks the .git marker and the registry round trip, which
// already bind that root. Reading core.worktree or core.bare here would add no
// further binding, because this path anchors to the worktree root itself and
// never uses what the config says; it would only turn one repository's
// unreadable config into a server startup failure. So this path deliberately
// skips the config. linkedWorktree must read it, because it answers a different
// question - where the main checkout is, for worktree.main_checkout.
func validateActiveWorktree(worktreeRoot, markerPath, gitDir string) error {
	commonDir, valid := commonGitDir(gitDir)
	if !valid {
		return fmt.Errorf("active git marker %q does not reference a worktree registry", markerPath)
	}
	canonicalCommon, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		return fmt.Errorf("resolve active worktree common git directory %q: %w", commonDir, err)
	}
	if filepath.Clean(canonicalCommon) != filepath.Clean(commonDir) {
		return fmt.Errorf("active worktree common git directory %q is not canonical", commonDir)
	}
	resolver := newPathResolver(context.Background())
	resolvedGitDir, state, err := resolver.resolveCommonGitRegistryEntry(canonicalCommon, gitDir)
	if err != nil {
		return fmt.Errorf("validate active worktree registry: %w", err)
	}
	if state != pathResolved {
		return fmt.Errorf("active worktree registry entry %q is unsafe or missing", gitDir)
	}
	backMarkerPath := filepath.Join(resolvedGitDir, "gitdir")
	backMarker, err := readRequiredMarker(backMarkerPath)
	if err != nil {
		return fmt.Errorf("validate active worktree back-reference: %w", err)
	}
	backReference, ok := markerLine(backMarker)
	if !ok {
		return fmt.Errorf("active worktree back-reference %q must contain one line", backMarkerPath)
	}
	backReference = resolveMarkerPath(backMarkerPath, backReference)
	if !pathNamesEqual(filepath.Base(backReference), ".git") {
		return fmt.Errorf("active worktree back-reference %q is not a .git marker", backMarkerPath)
	}
	resolvedMarker, state, err := resolver.resolveWithin(
		worktreeRoot,
		backReference,
		resolvedRegularFile,
	)
	if err != nil {
		return fmt.Errorf("validate active worktree back-reference: %w", err)
	}
	if state != pathResolved {
		return fmt.Errorf(
			"active worktree back-reference %q is unsafe or outside its root",
			backMarkerPath,
		)
	}
	canonical, err := resolver.isCanonicalGitMarker(resolvedMarker)
	if err != nil {
		return fmt.Errorf("validate active worktree marker: %w", err)
	}
	sameMarker, err := sameResolvedPath(resolvedMarker, markerPath)
	if err != nil {
		return fmt.Errorf("validate active worktree marker identity: %w", err)
	}
	if !canonical || !sameMarker {
		return fmt.Errorf("active worktree registry does not point back to %q", markerPath)
	}
	return nil
}

func readRequiredMarker(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect marker %q: %w", path, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("marker %q is not a regular file", path)
	}
	if info.Size() > maxGitMarkerBytes {
		return "", fmt.Errorf("marker %q exceeds %d bytes", path, maxGitMarkerBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read marker %q: %w", path, err)
	}
	if len(content) > maxGitMarkerBytes {
		return "", fmt.Errorf("marker %q exceeds %d bytes", path, maxGitMarkerBytes)
	}
	return string(content), nil
}

func resolveMarkerPath(markerPath, value string) string {
	if !filepath.IsAbs(value) {
		value = filepath.Join(filepath.Dir(markerPath), value)
	}
	return filepath.Clean(value)
}

func canonicalPathWithMissing(path string) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve active directory %q: %w", path, resolveErr)
			}
			for _, component := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, component)
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect active directory %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve active directory %q: %w", path, err)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func commonGitDir(worktreeGitDir string) (string, bool) {
	entryDir := filepath.Clean(worktreeGitDir)
	worktreesDir := filepath.Dir(entryDir)
	if !pathNamesEqual(filepath.Base(worktreesDir), "worktrees") {
		return "", false
	}
	commonDir := filepath.Dir(worktreesDir)
	if commonDir == filepath.Dir(commonDir) {
		return "", false
	}
	return commonDir, true
}

func readMarker(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect marker %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxGitMarkerBytes {
		return "", false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read marker %q: %w", path, err)
	}
	if len(content) > maxGitMarkerBytes {
		return "", false, nil
	}
	return string(content), true, nil
}

func markerLine(marker string) (string, bool) {
	line := strings.TrimRightFunc(marker, unicode.IsSpace)
	return line, line != "" && !strings.ContainsAny(line, "\r\n")
}

func isPlainDir(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect directory %q: %w", path, err)
	}
	return info.IsDir() && info.Mode()&fs.ModeSymlink == 0, nil
}

func newPathResolver(ctx context.Context) *pathResolver {
	// The operation scope reuses listings without carrying stale entries into a
	// later discovery.
	return &pathResolver{
		ctx:      ctx,
		listings: make(map[string]*directoryListing),
	}
}

func (r *pathResolver) seed(path string, entries []fs.DirEntry) error {
	path = filepath.Clean(path)
	if _, cached := r.listings[path]; cached {
		return nil
	}
	listing, err := buildDirectoryListing(r.ctx, entries)
	if err != nil {
		return err
	}
	r.listings[path] = listing
	return nil
}

func buildDirectoryListing(
	ctx context.Context,
	entries []fs.DirEntry,
) (*directoryListing, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("build directory listing: %w", err)
	}
	listing := &directoryListing{
		exact:     make(map[string]fs.DirEntry, len(entries)),
		folded:    make(map[string][]fs.DirEntry, len(entries)),
		inspected: make(map[string]inspectedEntry, len(entries)),
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("build directory listing: %w", err)
		}
		listing.exact[entry.Name()] = entry
		if runtime.GOOS == "windows" {
			key := foldedPathName(entry.Name())
			listing.folded[key] = append(listing.folded[key], entry)
		}
	}
	return listing, nil
}

func (r *pathResolver) listing(path string) *directoryListing {
	path = filepath.Clean(path)
	if listing, cached := r.listings[path]; cached {
		return listing
	}
	if err := r.ctx.Err(); err != nil {
		listing := &directoryListing{err: err}
		r.listings[path] = listing
		return listing
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		listing := &directoryListing{}
		if errors.Is(err, fs.ErrNotExist) {
			listing.missing = true
		} else {
			listing.err = fmt.Errorf("read directory %q: %w", path, err)
		}
		r.listings[path] = listing
		return listing
	}
	listing, buildErr := buildDirectoryListing(r.ctx, entries)
	if buildErr != nil {
		listing = &directoryListing{err: buildErr}
	}
	r.listings[path] = listing
	return listing
}

func (r *pathResolver) entry(
	parent string,
	requested string,
) (string, fs.FileMode, pathState, error) {
	listing := r.listing(parent)
	if listing.err != nil {
		return "", 0, pathInvalid, listing.err
	}
	if listing.missing {
		return "", 0, pathInvalid, nil
	}
	entry, found := listing.exact[requested]
	if !found && runtime.GOOS == "windows" {
		matches := listing.folded[foldedPathName(requested)]
		if len(matches) > 1 {
			return "", 0, pathAmbiguous, nil
		}
		if len(matches) == 1 && strings.EqualFold(matches[0].Name(), requested) {
			entry = matches[0]
			found = true
		}
	}
	if !found {
		return "", 0, pathInvalid, nil
	}
	return inspectResolvedEntry(parent, listing, entry)
}

func (r *pathResolver) exactEntry(
	parent string,
	requested string,
) (string, fs.FileMode, pathState, error) {
	listing := r.listing(parent)
	if listing.err != nil {
		return "", 0, pathInvalid, listing.err
	}
	if listing.missing {
		return "", 0, pathInvalid, nil
	}
	entry, found := listing.exact[requested]
	if !found {
		return "", 0, pathInvalid, nil
	}
	return inspectResolvedEntry(parent, listing, entry)
}

func inspectResolvedEntry(
	parent string,
	listing *directoryListing,
	entry fs.DirEntry,
) (string, fs.FileMode, pathState, error) {
	if inspected, cached := listing.inspected[entry.Name()]; cached {
		return entry.Name(), inspected.mode, inspected.state, inspected.err
	}
	info, err := entry.Info()
	inspected := inspectedEntry{state: pathResolved}
	if err != nil {
		inspected.state = pathInvalid
		if !errors.Is(err, fs.ErrNotExist) {
			inspected.err = fmt.Errorf(
				"inspect directory entry %q: %w",
				filepath.Join(parent, entry.Name()),
				err,
			)
		}
	} else {
		inspected.mode = info.Mode()
		if info.Mode()&fs.ModeSymlink != 0 {
			inspected.state = pathSymlink
		}
	}
	listing.inspected[entry.Name()] = inspected
	return entry.Name(), inspected.mode, inspected.state, inspected.err
}

func (r *pathResolver) resolveExactChild(
	parent string,
	name string,
	kind resolvedPathKind,
) (string, pathState, error) {
	resolvedName, mode, state, err := r.exactEntry(parent, name)
	if err != nil || state != pathResolved {
		return "", state, err
	}
	if kind == resolvedDirectory && !mode.IsDir() {
		return "", pathInvalid, nil
	}
	if kind == resolvedRegularFile && !mode.IsRegular() {
		return "", pathInvalid, nil
	}
	return filepath.Join(parent, resolvedName), pathResolved, nil
}

func (r *pathResolver) isCanonicalGitMarker(path string) (bool, error) {
	canonical, state, err := r.resolveExactChild(
		filepath.Dir(path),
		".git",
		resolvedRegularFile,
	)
	if err != nil {
		return false, err
	}
	if state != pathResolved {
		return false, nil
	}
	return sameResolvedPath(canonical, path)
}

func (r *pathResolver) resolveCommonGitRegistryEntry(
	commonGitDir string,
	requested string,
) (string, pathState, error) {
	resolved, state, err := r.resolveWithin(commonGitDir, requested, resolvedDirectory)
	if err != nil || state != pathResolved {
		return "", state, err
	}
	worktreesDir, state, err := r.resolveExactChild(
		commonGitDir,
		"worktrees",
		resolvedDirectory,
	)
	if err != nil || state != pathResolved {
		return "", state, err
	}
	sameRegistry, err := sameResolvedPath(filepath.Dir(resolved), worktreesDir)
	if err != nil {
		return "", pathInvalid, err
	}
	if !sameRegistry {
		return "", pathInvalid, nil
	}
	return resolved, pathResolved, nil
}

func sameResolvedPath(left, right string) (bool, error) {
	if left == right {
		return true, nil
	}
	leftInfo, err := os.Lstat(left)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect resolved path: %w", err)
	}
	rightInfo, err := os.Lstat(right)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect resolved path: %w", err)
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func (r *pathResolver) resolveWithin(
	boundary string,
	path string,
	kind resolvedPathKind,
) (string, pathState, error) {
	components, state, err := r.relativeComponentsWithin(boundary, path)
	if err != nil || state != pathResolved {
		return "", state, err
	}
	current := filepath.Clean(boundary)
	if len(components) == 0 {
		if kind == resolvedDirectory {
			return current, pathResolved, nil
		}
		return "", pathInvalid, nil
	}
	for index, component := range components {
		if err := r.ctx.Err(); err != nil {
			return "", pathInvalid, fmt.Errorf("resolve path within boundary: %w", err)
		}
		name, mode, entryState, entryErr := r.entry(current, component)
		if entryErr != nil || entryState != pathResolved {
			return "", entryState, entryErr
		}
		last := index == len(components)-1
		if !last || kind == resolvedDirectory {
			if !mode.IsDir() {
				return "", pathInvalid, nil
			}
		} else if !mode.IsRegular() {
			return "", pathInvalid, nil
		}
		current = filepath.Join(current, name)
	}
	if !canonicalPathWithin(boundary, current) {
		return "", pathOutside, nil
	}
	return current, pathResolved, nil
}

func (r *pathResolver) relativeComponentsWithin(
	boundary string,
	path string,
) ([]string, pathState, error) {
	boundaryVolume, boundaryParts, boundaryAbsolute := absolutePathParts(boundary)
	pathVolume, pathParts, pathAbsolute := absolutePathParts(path)
	if !boundaryAbsolute || !pathAbsolute || !pathNamesEqual(boundaryVolume, pathVolume) {
		return nil, pathOutside, nil
	}
	if len(pathParts) < len(boundaryParts) {
		return nil, pathOutside, nil
	}
	parent := volumeRoot(boundaryVolume)
	for index, boundaryPart := range boundaryParts {
		pathPart := pathParts[index]
		if pathPart == boundaryPart {
			parent = filepath.Join(parent, boundaryPart)
			continue
		}
		if runtime.GOOS != "windows" || !strings.EqualFold(pathPart, boundaryPart) {
			return nil, pathOutside, nil
		}
		name, mode, state, err := r.entry(parent, pathPart)
		if err != nil || state != pathResolved {
			return nil, state, err
		}
		if name != boundaryPart || !mode.IsDir() {
			return nil, pathOutside, nil
		}
		parent = filepath.Join(parent, boundaryPart)
	}
	return pathParts[len(boundaryParts):], pathResolved, nil
}

func absolutePathParts(path string) (string, []string, bool) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", nil, false
	}
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	remainder = strings.TrimLeft(remainder, string(filepath.Separator))
	if remainder == "" {
		return volume, nil, true
	}
	return volume, strings.Split(remainder, string(filepath.Separator)), true
}

func volumeRoot(volume string) string {
	return volume + string(filepath.Separator)
}

func canonicalPathWithin(parent, child string) bool {
	parentVolume, parentParts, parentAbsolute := absolutePathParts(parent)
	childVolume, childParts, childAbsolute := absolutePathParts(child)
	if !parentAbsolute || !childAbsolute || !pathNamesEqual(parentVolume, childVolume) {
		return false
	}
	if len(childParts) < len(parentParts) {
		return false
	}
	for index, part := range parentParts {
		if childParts[index] != part {
			return false
		}
	}
	return true
}

func pathNamesEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func foldedPathName(name string) string {
	var folded strings.Builder
	for _, current := range name {
		minimum := current
		for next := unicode.SimpleFold(current); next != current; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		folded.WriteRune(minimum)
	}
	return folded.String()
}
