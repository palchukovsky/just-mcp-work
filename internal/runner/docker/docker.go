// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

// Package docker implements Dockerfile and Docker Compose tasks.
package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

const (
	runnerName        = "docker"
	dockerfileName    = "Dockerfile"
	dockerBuildTaskID = runnerName + ":build"
	composeUpTaskID   = runnerName + ":compose:up"
	composeDownTaskID = runnerName + ":compose:down"
	composeServiceID  = composeUpTaskID + ":"
	buildTaskKind     = "build"
	composeTaskKind   = "compose"
	imageNamespace    = "jmw"
	imageTag          = "latest"
	fallbackImageName = "project"
	// digestBytes is the length of the project digest that keeps the image tags
	// of same-named projects apart. Eight hex characters stay readable and are
	// far more than a workspace needs to avoid a collision.
	digestBytes = 4
	// envFileName participates in the discovery fingerprint because Compose
	// interpolates it into the manifest before reporting the service list.
	envFileName = ".env"
	// includeKey is the top-level Compose key that pulls in manifests this
	// package cannot enumerate, which is what disables the discovery cache.
	includeKey = "include"
	// maxCachedProjects bounds the discovery cache of a long-lived server whose
	// workspace outlives many project directories.
	maxCachedProjects = 512
	profileKind       = "profile"
	serviceKind       = "service"
)

// composeFamily is one Docker Compose file naming family. Compose combines a
// base file only with an override of the same family, so the override of
// another family must never join the invocation.
type composeFamily struct {
	base      []string
	overrides []string
}

func composeFamilies() []composeFamily {
	return []composeFamily{
		{
			base:      []string{"compose.yaml", "compose.yml"},
			overrides: []string{"compose.override.yaml", "compose.override.yml"},
		},
		{
			base:      []string{"docker-compose.yaml", "docker-compose.yml"},
			overrides: []string{"docker-compose.override.yaml", "docker-compose.override.yml"},
		},
	}
}

// composeLayout is the Docker Compose service and profile listing of one
// project. Profiles are named explicitly on every invocation that must reach
// beyond the default set, so no Compose release has to understand a wildcard.
type composeLayout struct {
	services []string
	profiles []string
}

func (l composeLayout) clone() composeLayout {
	return composeLayout{
		services: slices.Clone(l.services),
		profiles: slices.Clone(l.profiles),
	}
}

// composeCache is the cached Compose listing of one project together with the
// fingerprint of the inputs it was produced from. Its mutex also serialises the
// listing itself, so concurrent scans of one project share a single
// "docker compose config" process instead of duplicating it.
type composeCache struct {
	err         error
	fingerprint string
	layout      composeLayout
	mutex       sync.Mutex
	valid       bool
}

// Runner executes Dockerfile builds and Docker Compose lifecycle tasks.
type Runner struct {
	commandContext func(context.Context, string, ...string) *exec.Cmd
	lookPath       func(string) (string, error)
	caches         map[string]*composeCache
	binary         string
	cachesMutex    sync.Mutex
}

// New constructs a Docker runner. An empty binary uses "docker" from PATH.
func New(binary string) *Runner {
	if binary == "" {
		binary = runnerName
	}
	return &Runner{
		binary:         binary,
		commandContext: exec.CommandContext,
		lookPath:       exec.LookPath,
		caches:         make(map[string]*composeCache),
	}
}

// Name returns the stable runner name.
func (*Runner) Name() string { return runnerName }

// command builds an argv-only Docker invocation rooted at projectDir. An empty
// projectDir leaves the working directory at the calling process default.
func (r *Runner) command(ctx context.Context, projectDir string, argv ...string) *exec.Cmd {
	newCommand := r.commandContext
	if newCommand == nil {
		newCommand = exec.CommandContext
	}
	// #nosec G204 -- binary is local config and argv is discovered metadata, never a shell.
	cmd := newCommand(ctx, r.binary, argv...)
	cmd.Dir = projectDir
	return cmd
}

// RunnerVersion reports the installed Docker version for run metadata.
func (r *Runner) RunnerVersion(ctx context.Context) (string, error) {
	output, err := r.command(ctx, "", "--version").Output()
	if err != nil {
		return "", fmt.Errorf("get Docker version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Detect reports whether projectDir holds Docker files. A missing Docker binary
// is reported by ListTasks instead, so a project whose only runner is Docker
// stays visible with a diagnosis rather than disappearing from discovery.
func (*Runner) Detect(projectDir string) (bool, error) {
	return hasDockerFiles(projectDir)
}

// checkInstalled reports whether the configured Docker binary resolves on PATH.
// The failure is marked as a missing tool, not as a broken project: a checkout
// that carries a Dockerfile is common on hosts without Docker.
func (r *Runner) checkInstalled() error {
	lookPath := r.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(r.binary); err != nil {
		return fmt.Errorf(
			"find the Docker binary %q: %w: %w",
			r.binary, runner.ErrToolUnavailable, err,
		)
	}
	return nil
}

// ListTasks discovers Dockerfile builds and Docker Compose lifecycle tasks. An
// unusable Compose manifest is reported together with the Dockerfile build
// task, which stays runnable and is often what repairs the manifest.
func (r *Runner) ListTasks(ctx context.Context, projectDir string) ([]runner.Task, error) {
	if err := r.checkInstalled(); err != nil {
		return nil, err
	}

	var tasks []runner.Task
	if _, err := findDockerfile(projectDir); err == nil {
		tasks = append(tasks, dockerfileTask(projectDir))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	composeFiles, err := findComposeFiles(projectDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sortTasks(tasks), nil
		}
		return sortTasks(tasks), err
	}
	layout, err := r.discoverCompose(ctx, projectDir, composeFiles)
	if err != nil {
		return sortTasks(tasks), err
	}
	return sortTasks(append(tasks, composeTasks(layout.services)...)), nil
}

func sortTasks(tasks []runner.Task) []runner.Task {
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})
	return tasks
}

// BuildCommand creates an argv-only invocation for a discovered Docker task.
// Arguments are placed where Docker expects options, before the positional
// build context or Compose service. Appending them at the end would turn a bare
// word into an extra image context or an extra started service.
func (r *Runner) BuildCommand(
	ctx context.Context,
	projectDir string,
	task runner.Task,
	args []string,
) (*exec.Cmd, error) {
	if task.Runner != runnerName {
		return nil, fmt.Errorf("task %q does not belong to the %s runner", task.ID, runnerName)
	}
	var (
		argv []string
		err  error
	)
	switch {
	case task.ID == dockerBuildTaskID:
		argv, err = buildCommand(projectDir, args)
	case task.ID == composeUpTaskID:
		argv, err = composeUpCommand(projectDir, "", args)
	case task.ID == composeDownTaskID:
		argv, err = r.composeDownCommand(ctx, projectDir, args)
	case strings.HasPrefix(task.ID, composeServiceID):
		service := strings.TrimPrefix(task.ID, composeServiceID)
		if !validComposeName(service) {
			return nil, fmt.Errorf("task %q has an invalid Docker Compose service", task.ID)
		}
		argv, err = composeUpCommand(projectDir, service, args)
	default:
		return nil, fmt.Errorf("task %q has an unsupported Docker action", task.ID)
	}
	if err != nil {
		return nil, err
	}
	return r.command(ctx, projectDir, argv...), nil
}

// buildCommand builds the Dockerfile under a tag that is stable for one project
// directory, so repeated builds replace that one image instead of leaving a
// dangling image per run, and two same-named projects never overwrite each
// other's image.
func buildCommand(projectDir string, args []string) ([]string, error) {
	dockerfilePath, err := findDockerfile(projectDir)
	if err != nil {
		return nil, err
	}
	argv := []string{"build", "--file", dockerfilePath, "--tag", imageReference(projectDir)}
	argv = append(argv, args...)
	return append(argv, "."), nil
}

// composeUpCommand starts the services Docker Compose starts by default. A
// service behind a profile stays out: it is discovered as its own task, and
// enabling every profile here would also run opt-in one-shot services such as
// migrations. Compose enables the profiles of an explicitly named service by
// itself, so the service task needs no profile flag either.
func composeUpCommand(projectDir string, service string, args []string) ([]string, error) {
	composeFiles, err := findComposeFiles(projectDir)
	if err != nil {
		return nil, err
	}
	argv := append(composeCommand(composeFiles, nil), "up", "--detach")
	argv = append(argv, args...)
	if service != "" {
		argv = append(argv, service)
	}
	return argv, nil
}

// composeDownCommand stops the whole project. Every declared profile is named
// explicitly because "down" leaves the containers of profile services running
// otherwise, and those services are startable through their own tasks.
func (r *Runner) composeDownCommand(
	ctx context.Context,
	projectDir string,
	args []string,
) ([]string, error) {
	composeFiles, err := findComposeFiles(projectDir)
	if err != nil {
		return nil, err
	}
	layout, err := r.discoverCompose(ctx, projectDir, composeFiles)
	if err != nil {
		return nil, err
	}
	argv := append(composeCommand(composeFiles, layout.profiles), "down")
	return append(argv, args...), nil
}

// discoverCompose reports the Compose profiles and services of projectDir,
// reusing the last result while the discovery inputs are unchanged. Discovery
// runs for every project of a scan, and "compose config" costs a Docker CLI
// process that parses and interpolates the whole manifest. A failed listing is
// cached too: a manifest that cannot be read stays unreadable until it changes,
// and re-running the failure on every scan only multiplies the cost.
func (r *Runner) discoverCompose(
	ctx context.Context,
	projectDir string,
	composeFiles []string,
) (composeLayout, error) {
	fingerprint, cacheable := composeFingerprint(projectDir, composeFiles)
	cache := r.projectCache(projectDir)
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if cacheable && cache.valid && cache.fingerprint == fingerprint {
		return cache.layout.clone(), cache.err
	}
	layout, err := r.listCompose(ctx, projectDir, composeFiles)
	cache.valid = cacheable && cacheableResult(ctx, err)
	cache.fingerprint = fingerprint
	cache.layout = layout.clone()
	cache.err = err
	return layout, err
}

// cacheableResult reports whether a listing outcome describes the manifest
// rather than the call that read it. A run that was cancelled or that hit the
// deadline of its scan says nothing about the project, and caching it would
// keep reporting a broken manifest until a Compose file happens to change.
func cacheableResult(ctx context.Context, err error) bool {
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func (r *Runner) projectCache(projectDir string) *composeCache {
	r.cachesMutex.Lock()
	defer r.cachesMutex.Unlock()
	if r.caches == nil {
		r.caches = make(map[string]*composeCache)
	}
	if cache, found := r.caches[projectDir]; found {
		return cache
	}
	r.evictLocked()
	cache := &composeCache{}
	r.caches[projectDir] = cache
	return cache
}

// evictLocked bounds the cache. Entries of directories that disappeared go
// first; a cache that is still full afterwards is dropped as a whole, which
// costs one listing per project and never leaks.
func (r *Runner) evictLocked() {
	if len(r.caches) < maxCachedProjects {
		return
	}
	for projectDir := range r.caches {
		if info, err := os.Lstat(projectDir); err != nil || !info.IsDir() {
			delete(r.caches, projectDir)
		}
	}
	if len(r.caches) >= maxCachedProjects {
		clear(r.caches)
	}
}

// composeFingerprint describes the inputs of the Compose listing: the contents
// of the Compose files, the contents of the interpolated environment file, and
// the environment of this process, which Compose interpolates as well. A file
// that is absent is recorded as such, so creating it invalidates the cache too.
//
// The result is cacheable only when every input is enumerable. A manifest with
// a top-level "include" pulls in files this package does not know about, so its
// listing is recomputed instead of being trusted.
func composeFingerprint(projectDir string, composeFiles []string) (string, bool) {
	var inputs strings.Builder
	cacheable := true
	paths := append(slices.Clone(composeFiles), filepath.Join(projectDir, envFileName))
	for _, path := range paths {
		inputs.WriteString(path + "\n")
		// #nosec G304 -- path is a fixed Compose filename below projectDir.
		contents, err := os.ReadFile(path)
		if err != nil {
			inputs.WriteString("unreadable\n")
			if !errors.Is(err, os.ErrNotExist) {
				cacheable = false
			}
			continue
		}
		fileDigest := sha256.Sum256(contents)
		inputs.WriteString(hex.EncodeToString(fileDigest[:]) + "\n")
		if declaresInclude(contents) {
			cacheable = false
		}
	}
	environment := os.Environ()
	sort.Strings(environment)
	for _, variable := range environment {
		inputs.WriteString(variable + "\n")
	}
	digest := sha256.Sum256([]byte(inputs.String()))
	return hex.EncodeToString(digest[:]), cacheable
}

// declaresInclude reports a top-level "include" key. Compose resolves it into
// manifests outside this directory, and their contents cannot join the
// fingerprint without parsing the manifest here.
//
// The manifest is matched line by line rather than parsed: the key is accepted
// with optional quoting and with spaces before its colon, and a nested or
// commented occurrence is not distinguished from a real one. A false positive
// only turns the cache off for that project, which stays correct.
func declaresInclude(contents []byte) bool {
	for line := range strings.Lines(string(contents)) {
		key, _, found := strings.Cut(strings.TrimRight(line, "\r\n"), ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		key = strings.Trim(key, `"'`)
		if key == includeKey {
			return true
		}
	}
	return false
}

// listCompose asks Compose for the declared profiles first, then lists the
// services with those profiles enabled, so services behind a profile become
// discoverable without relying on a wildcard profile name.
func (r *Runner) listCompose(
	ctx context.Context,
	projectDir string,
	composeFiles []string,
) (composeLayout, error) {
	profiles, err := r.listComposeNames(
		ctx, projectDir, composeCommand(composeFiles, nil), "--profiles", profileKind,
	)
	if err != nil {
		return composeLayout{}, err
	}
	services, err := r.listComposeNames(
		ctx, projectDir, composeCommand(composeFiles, profiles), "--services", serviceKind,
	)
	if err != nil {
		return composeLayout{}, err
	}
	return composeLayout{services: services, profiles: profiles}, nil
}

func (r *Runner) listComposeNames(
	ctx context.Context,
	projectDir string,
	base []string,
	selector string,
	kind string,
) ([]string, error) {
	argv := append(slices.Clone(base), "config", selector)
	output, err := r.command(ctx, projectDir, argv...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			details := strings.TrimSpace(string(exitErr.Stderr))
			if missingComposePlugin(details) {
				return nil, fmt.Errorf(
					"run %q: %w: %s",
					r.binary+" compose", runner.ErrToolUnavailable, details,
				)
			}
			if details == "" {
				return nil, fmt.Errorf("list Docker Compose %ss: %w", kind, err)
			}
			return nil, fmt.Errorf("list Docker Compose %ss: %w: %s", kind, err, details)
		}
		return nil, fmt.Errorf(
			"start Docker Compose %s listing: %w",
			kind, runner.MarkMissingTool(r.binary, err),
		)
	}
	return parseNames(string(output), kind)
}

// missingComposePlugin recognises the Docker CLI refusing "compose" itself. The
// binary is installed, only the Compose v2 plugin is not, which is the same
// kind of host gap as a missing Docker: the checkout is fine and the Dockerfile
// build still runs, so this must not fail the whole project.
func missingComposePlugin(details string) bool {
	lowered := strings.ToLower(details)
	if !strings.Contains(lowered, "compose") {
		return false
	}
	return strings.Contains(lowered, "not a docker command") ||
		strings.Contains(lowered, "unknown command")
}

func composeCommand(composeFiles []string, profiles []string) []string {
	argv := []string{"compose"}
	for _, composeFile := range composeFiles {
		argv = append(argv, "--file", composeFile)
	}
	for _, profile := range profiles {
		argv = append(argv, "--profile", profile)
	}
	return argv
}

func dockerfileTask(projectDir string) runner.Task {
	image := imageReference(projectDir)
	return runner.Task{
		ID:     dockerBuildTaskID,
		Runner: runnerName,
		Name:   "build",
		Description: "Build the project Dockerfile as " + image + ". " +
			"Arguments are passed to \"docker build\" as options, before the build context.",
		Meta: map[string]any{
			"image": image,
			"kind":  buildTaskKind,
		},
	}
}

func composeTasks(services []string) []runner.Task {
	tasks := []runner.Task{
		{
			ID:     composeUpTaskID,
			Runner: runnerName,
			Name:   "compose up",
			Description: "Start the default Docker Compose services in detached mode. " +
				"A service behind a profile has its own task. " +
				"Arguments are passed to \"compose up\" as options; " +
				"a bare word is taken by Compose as another service to start.",
			Meta: map[string]any{
				"kind":    composeTaskKind,
				"service": "",
			},
		},
		{
			ID:     composeDownTaskID,
			Runner: runnerName,
			Name:   "compose down",
			Description: "Stop and remove the Docker Compose services of this project, " +
				"including services behind a profile. " +
				"Arguments are passed to \"compose down\" as options.",
			Meta: map[string]any{
				"kind":    composeTaskKind,
				"service": "",
			},
		},
	}
	for _, service := range services {
		tasks = append(tasks, runner.Task{
			ID:     composeServiceID + service,
			Runner: runnerName,
			Name:   "compose up " + service,
			Description: "Start Docker Compose service " + service + " in detached mode. " +
				"Arguments are passed to \"compose up\" as options; " +
				"a bare word is taken by Compose as another service to start.",
			Meta: map[string]any{
				"kind":    composeTaskKind,
				"service": service,
			},
		})
	}
	return tasks
}

func parseNames(output string, kind string) ([]string, error) {
	unique := make(map[string]struct{})
	for line := range strings.Lines(output) {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if !validComposeName(name) {
			return nil, fmt.Errorf("invalid Docker Compose %s %q", kind, name)
		}
		unique[name] = struct{}{}
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// validComposeName accepts the service and profile names that can safely become
// a task identifier and an argv word.
func validComposeName(name string) bool {
	return name != "" &&
		!strings.HasPrefix(name, "-") &&
		!strings.ContainsAny(name, " \t\r\n:")
}

// imageReference names the image built from the project Dockerfile. The jmw
// namespace keeps that tag away from the images a project publishes itself, and
// the project digest keeps two same-named project directories from replacing
// each other's image.
func imageReference(projectDir string) string {
	return imageNamespace + "/" +
		imageName(projectDir) + "-" + projectDigest(projectDir) +
		":" + imageTag
}

// imageName maps the project directory name to a Docker repository component.
func imageName(projectDir string) string {
	var name strings.Builder
	separator := false
	for _, symbol := range strings.ToLower(filepath.Base(projectDir)) {
		if (symbol < 'a' || symbol > 'z') && (symbol < '0' || symbol > '9') {
			separator = name.Len() > 0
			continue
		}
		if separator {
			name.WriteByte('-')
			separator = false
		}
		name.WriteRune(symbol)
	}
	if name.Len() == 0 {
		return fallbackImageName
	}
	return name.String()
}

// projectDigest identifies the project directory itself. It is a name, not a
// security boundary: it only has to be stable for one path and different for
// two paths that share a directory name.
func projectDigest(projectDir string) string {
	digest := sha256.Sum256([]byte(filepath.ToSlash(filepath.Clean(projectDir))))
	return hex.EncodeToString(digest[:digestBytes])
}

func findDockerfile(projectDir string) (string, error) {
	path, err := runner.FindRegularFile(projectDir, dockerfileName)
	if err != nil {
		return "", fmt.Errorf("find %s in %q: %w", dockerfileName, projectDir, err)
	}
	return path, nil
}

// findComposeFiles reports the Compose files of projectDir in invocation order.
// The override file is only looked up within the family of the base file, the
// way Compose itself pairs them.
func findComposeFiles(projectDir string) ([]string, error) {
	for _, family := range composeFamilies() {
		baseFile, err := runner.FindRegularFile(projectDir, family.base...)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("find Docker Compose file in %q: %w", projectDir, err)
		}
		composeFiles := []string{baseFile}

		overrideFile, err := runner.FindRegularFile(projectDir, family.overrides...)
		if err == nil {
			return append(composeFiles, overrideFile), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf(
				"find Docker Compose override file in %q: %w",
				projectDir,
				err,
			)
		}
		return composeFiles, nil
	}
	return nil, fmt.Errorf(
		"find Docker Compose file in %q: %w",
		projectDir,
		os.ErrNotExist,
	)
}

func hasDockerFiles(projectDir string) (bool, error) {
	if _, err := findDockerfile(projectDir); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	_, err := findComposeFiles(projectDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
