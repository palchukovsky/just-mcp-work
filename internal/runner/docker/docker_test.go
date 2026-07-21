// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

// newTestRunner returns a runner whose Docker binary always resolves, so file
// discovery is tested without depending on the host installation.
func newTestRunner() *Runner {
	r := New("docker")
	r.lookPath = func(binary string) (string, error) { return "/usr/bin/" + binary, nil }
	return r
}

// composeStub answers "docker compose config" from fixed name lists and records
// every invocation, so discovery is tested without a Docker installation.
type composeStub struct {
	calls [][]string
}

// stubCompose makes r answer the profile and service listings from the given
// lists. The selector of the recorded invocation decides which list is served.
func stubCompose(t *testing.T, r *Runner, profiles []string, services []string) *composeStub {
	t.Helper()
	helperBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stub := &composeStub{}
	r.commandContext = func(ctx context.Context, binary string, args ...string) *exec.Cmd {
		// Only the listing is served from the fixtures. Every other invocation
		// is built for real, so the tests keep asserting the whole argv.
		if len(args) < 2 || args[len(args)-2] != "config" {
			// #nosec G204 -- the runner binary and argv of the test fixtures.
			return exec.CommandContext(ctx, binary, args...)
		}
		stub.calls = append(stub.calls, append([]string(nil), args...))
		names := services
		if args[len(args)-1] == "--profiles" {
			names = profiles
		}
		// #nosec G204 -- fixed Go test binary and helper selector.
		cmd := exec.CommandContext(ctx, helperBinary, "-test.run=^TestDockerListHelper$")
		cmd.Env = append(
			os.Environ(),
			"JMW_DOCKER_HELPER=list",
			"JMW_DOCKER_LINES="+strings.Join(names, "\n"),
		)
		return cmd
	}
	return stub
}

func TestDockerListHelper(_ *testing.T) {
	if os.Getenv("JMW_DOCKER_HELPER") != "list" {
		return
	}
	output := os.Getenv("JMW_DOCKER_LINES")
	if output != "" {
		output += "\n"
	}
	if _, err := os.Stdout.WriteString(output); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestDockerVersionHelper(_ *testing.T) {
	if os.Getenv("JMW_DOCKER_HELPER") != "version" {
		return
	}
	if _, err := os.Stdout.WriteString("Docker version 42.0.0, build test\n"); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestDetectRecognizesDockerFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{dockerfileName, "compose.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		detected, err := newTestRunner().Detect(dir)
		if err != nil {
			t.Fatalf("Detect(%s): %v", name, err)
		}
		if !detected {
			t.Fatalf("Detect(%s) = false, want true", name)
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectIgnoresDirectoryWithoutDockerFiles(t *testing.T) {
	detected, err := newTestRunner().Detect(t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if detected {
		t.Fatal("Detect accepted a directory without Docker files")
	}
}

// TestMissingDockerBinaryIsReportedNotHidden keeps a Docker-only project visible
// in discovery: Detect still accepts it and ListTasks explains the missing
// binary as an unavailable tool, which the workspace reports as a warning
// instead of a broken project.
func TestMissingDockerBinaryIsReportedNotHidden(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "compose.yaml"),
		[]byte("services: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	r := New("")
	r.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	detected, err := r.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !detected {
		t.Fatal("Detect hid a Docker project instead of reporting the missing binary")
	}

	_, err = r.ListTasks(context.Background(), dir)
	if err == nil {
		t.Fatal("ListTasks accepted a project without the Docker binary")
	}
	if !strings.Contains(err.Error(), "Docker binary") {
		t.Fatalf("ListTasks error = %v, want the missing Docker binary", err)
	}
	if !errors.Is(err, runner.ErrToolUnavailable) {
		t.Fatalf("ListTasks error = %v, want runner.ErrToolUnavailable", err)
	}
}

func TestDetectRejectsSymlinkedDockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source", filepath.Join(dir, dockerfileName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	detected, err := newTestRunner().Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if detected {
		t.Fatal("Detect accepted a symlinked Dockerfile")
	}
}

func TestListTasksIncludesDockerfileBuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, dockerfileName),
		[]byte("FROM scratch\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	tasks, err := newTestRunner().ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if got, want := tasks, []runner.Task{dockerfileTask(dir)}; !reflect.DeepEqual(
		got,
		want,
	) {
		t.Fatalf("ListTasks = %#v, want %#v", got, want)
	}
}

func TestListTasksIncludesComposeServicesFromDefaultFiles(t *testing.T) {
	dir := t.TempDir()
	for name, contents := range map[string]string{
		dockerfileName:         "FROM scratch\n",
		"compose.yaml":         "services: {}\n",
		"compose.override.yml": "services: {}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r := newTestRunner()
	stub := stubCompose(t, r, []string{"debug"}, []string{"worker", "api"})

	tasks, err := r.ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	wantIDs := []string{
		"docker:build",
		"docker:compose:down",
		"docker:compose:up",
		"docker:compose:up:api",
		"docker:compose:up:worker",
	}
	if got := taskIDs(tasks); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("ListTasks IDs = %#v, want %#v", got, wantIDs)
	}

	files := []string{
		"compose",
		"--file",
		filepath.Join(dir, "compose.yaml"),
		"--file",
		filepath.Join(dir, "compose.override.yml"),
	}
	wantCalls := [][]string{
		append(append([]string(nil), files...), "config", "--profiles"),
		append(append([]string(nil), files...), "--profile", "debug", "config", "--services"),
	}
	if !reflect.DeepEqual(stub.calls, wantCalls) {
		t.Fatalf("Docker calls = %#v, want %#v", stub.calls, wantCalls)
	}
}

// TestListTasksKeepsBuildTaskWhenComposeFails covers the project whose Compose
// manifest cannot be listed: the Dockerfile build stays runnable, and it is
// often the task that repairs the manifest.
func TestListTasksKeepsBuildTaskWhenComposeFails(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{dockerfileName, "compose.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := newTestRunner()
	r.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// #nosec G204 -- fixed missing-binary path used to exercise the error path.
		return exec.CommandContext(ctx, filepath.Join(dir, "absent-docker"))
	}

	tasks, err := r.ListTasks(context.Background(), dir)
	if err == nil {
		t.Fatal("ListTasks hid a failing Docker Compose listing")
	}
	if got, want := taskIDs(tasks), []string{dockerBuildTaskID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListTasks IDs = %#v, want %#v", got, want)
	}
}

func TestListTasksReportsUnavailableDockerBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "compose.yaml"),
		[]byte("services: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	r := newTestRunner()
	r.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// #nosec G204 -- fixed missing-binary path used to exercise the error path.
		return exec.CommandContext(ctx, filepath.Join(dir, "absent-docker"))
	}

	_, err := r.ListTasks(context.Background(), dir)
	if err == nil {
		t.Fatal("ListTasks accepted a missing Docker binary")
	}
	if !strings.Contains(err.Error(), "start Docker Compose profile listing") {
		t.Fatalf("ListTasks error = %v, want a profile listing failure", err)
	}
}

func TestRunnerVersionReportsDockerVersion(t *testing.T) {
	helperBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRunner()
	r.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// #nosec G204 -- fixed Go test binary and helper selector.
		cmd := exec.CommandContext(ctx, helperBinary, "-test.run=^TestDockerVersionHelper$")
		cmd.Env = append(os.Environ(), "JMW_DOCKER_HELPER=version")
		return cmd
	}

	version, err := r.RunnerVersion(context.Background())
	if err != nil {
		t.Fatalf("RunnerVersion: %v", err)
	}
	if version != "Docker version 42.0.0, build test" {
		t.Fatalf("RunnerVersion = %q", version)
	}
}

func TestComposeTasksNameServices(t *testing.T) {
	tasks := composeTasks([]string{"worker", "api"})
	if got, want := taskIDs(tasks), []string{
		"docker:compose:up",
		"docker:compose:down",
		"docker:compose:up:worker",
		"docker:compose:up:api",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("composeTasks IDs = %#v, want %#v", got, want)
	}
}

func TestParseNamesRejectsInvalidName(t *testing.T) {
	for _, name := range []string{"invalid:name", "-api"} {
		if _, err := parseNames("api\n"+name+"\n", serviceKind); err == nil {
			t.Fatalf("parseNames accepted invalid name %q", name)
		}
	}
}

func TestParseNamesSortsAndDeduplicates(t *testing.T) {
	names, err := parseNames("worker\napi\nworker\n", serviceKind)
	if err != nil {
		t.Fatalf("parseNames: %v", err)
	}
	if got, want := names, []string{"api", "worker"}; !reflect.DeepEqual(
		got,
		want,
	) {
		t.Fatalf("parseNames = %#v, want %#v", got, want)
	}
}

func TestImageReferenceNormalizesProjectDirectory(t *testing.T) {
	for projectDir, wantName := range map[string]string{
		filepath.Join("workspace", "My Project"): "my-project",
		filepath.Join("workspace", "api_v2"):     "api-v2",
		filepath.Join("workspace", "--"):         "project",
	} {
		got := imageReference(projectDir)
		wantPrefix := "jmw/" + wantName + "-"
		if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, ":latest") {
			t.Fatalf("imageReference(%q) = %q, want %q…:latest", projectDir, got, wantPrefix)
		}
		if digest := strings.TrimSuffix(strings.TrimPrefix(got, wantPrefix), ":latest"); len(
			digest,
		) != 2*digestBytes {
			t.Fatalf("imageReference(%q) digest = %q", projectDir, digest)
		}
	}
}

// TestImageReferenceSeparatesSameNamedProjects covers two workspace projects
// whose directories share a name: a build of one must not replace the image of
// the other.
func TestImageReferenceSeparatesSameNamedProjects(t *testing.T) {
	first := imageReference(filepath.Join("workspace", "frontend", "api"))
	second := imageReference(filepath.Join("workspace", "backend", "api"))
	if first == second {
		t.Fatalf("same-named projects share the image reference %q", first)
	}
	if repeated := imageReference(filepath.Join("workspace", "frontend", "api")); repeated != first {
		t.Fatalf("imageReference is unstable: %q, then %q", first, repeated)
	}
}

func TestBuildCommandCreatesDockerCommands(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{dockerfileName, "compose.yml", "compose.override.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	composeFiles := []string{
		"compose",
		"--file",
		filepath.Join(dir, "compose.yml"),
		"--file",
		filepath.Join(dir, "compose.override.yml"),
	}
	r := newTestRunner()
	stubCompose(t, r, []string{"debug", "seed"}, []string{"api"})
	tests := []struct {
		name string
		task runner.Task
		want []string
	}{
		{
			name: "build",
			task: dockerfileTask(dir),
			want: []string{
				"docker",
				"build",
				"--file",
				filepath.Join(dir, dockerfileName),
				"--tag",
				imageReference(dir),
				".",
			},
		},
		{
			name: "compose up",
			task: composeTasks(nil)[0],
			want: append(append([]string{"docker"}, composeFiles...), "up", "--detach"),
		},
		{
			name: "compose down",
			task: composeTasks(nil)[1],
			want: append(
				append([]string{"docker"}, composeFiles...),
				"--profile", "debug", "--profile", "seed", "down",
			),
		},
		{
			name: "compose service",
			task: composeTasks([]string{"api"})[2],
			want: append(append([]string{"docker"}, composeFiles...), "up", "--detach", "api"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := r.BuildCommand(context.Background(), dir, test.task, nil)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if got := cmd.Args; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("command args = %#v, want %#v", got, test.want)
			}
			if cmd.Dir != dir {
				t.Fatalf("command dir = %q, want %q", cmd.Dir, dir)
			}
		})
	}
}

func TestBuildCommandKeepsDefaultProfilesOnComposeUp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "compose.yaml"),
		[]byte("services: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	r := newTestRunner()
	stubCompose(t, r, []string{"debug"}, []string{"api"})
	cmd, err := r.BuildCommand(context.Background(), dir, composeTasks(nil)[0], nil)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if argv := strings.Join(cmd.Args, " "); strings.Contains(argv, "--profile") {
		t.Fatalf("compose up enabled every profile: %q", argv)
	}
}

// TestBuildCommandNamesNoProfileWithoutOne keeps the invocation of a project
// without profiles identical to a plain "docker compose down".
func TestBuildCommandNamesNoProfileWithoutOne(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "compose.yaml"),
		[]byte("services: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	r := newTestRunner()
	stubCompose(t, r, nil, []string{"api"})
	cmd, err := r.BuildCommand(context.Background(), dir, composeTasks(nil)[1], nil)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	want := []string{"docker", "compose", "--file", filepath.Join(dir, "compose.yaml"), "down"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("command args = %#v, want %#v", cmd.Args, want)
	}
}

func TestBuildCommandRejectsUnsupportedTask(t *testing.T) {
	dir := t.TempDir()
	for name, task := range map[string]runner.Task{
		"foreign runner": {ID: "make:build", Runner: "make"},
		"unknown action": {ID: runnerName + ":publish", Runner: runnerName},
		"empty service":  {ID: composeServiceID, Runner: runnerName},
	} {
		if _, err := newTestRunner().BuildCommand(
			context.Background(),
			dir,
			task,
			nil,
		); err == nil {
			t.Fatalf("BuildCommand accepted a task with an %s", name)
		}
	}
}

func TestBuildCommandReportsMissingProjectFiles(t *testing.T) {
	dir := t.TempDir()
	for name, expectations := range map[string]struct {
		task    runner.Task
		message string
	}{
		"build":   {task: dockerfileTask(dir), message: dockerfileName},
		"compose": {task: composeTasks(nil)[0], message: "Docker Compose file"},
	} {
		_, err := newTestRunner().BuildCommand(
			context.Background(),
			dir,
			expectations.task,
			nil,
		)
		if err == nil {
			t.Fatalf("BuildCommand(%s) accepted a project without its files", name)
		}
		if !strings.Contains(err.Error(), expectations.message) ||
			!strings.Contains(err.Error(), dir) {
			t.Fatalf("BuildCommand(%s) error = %v, want the missing file and project", name, err)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("BuildCommand(%s) error = %v, want os.ErrNotExist", name, err)
		}
	}
}

// TestFindComposeFilesKeepsOverrideFamily covers a project that carries the
// override of the other naming family: Compose would not combine those two, so
// neither may jmw.
func TestFindComposeFilesKeepsOverrideFamily(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"compose.yaml", "docker-compose.override.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	composeFiles, err := findComposeFiles(dir)
	if err != nil {
		t.Fatalf("findComposeFiles: %v", err)
	}
	if want := []string{filepath.Join(dir, "compose.yaml")}; !reflect.DeepEqual(composeFiles, want) {
		t.Fatalf("findComposeFiles = %#v, want %#v", composeFiles, want)
	}

	if writeErr := os.WriteFile(
		filepath.Join(dir, "compose.override.yaml"),
		[]byte("services: {}\n"),
		0o600,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	composeFiles, err = findComposeFiles(dir)
	if err != nil {
		t.Fatalf("findComposeFiles: %v", err)
	}
	want := []string{
		filepath.Join(dir, "compose.yaml"),
		filepath.Join(dir, "compose.override.yaml"),
	}
	if !reflect.DeepEqual(composeFiles, want) {
		t.Fatalf("findComposeFiles = %#v, want %#v", composeFiles, want)
	}
}

// TestBuildCommandPlacesArgumentsBeforePositionals pins the argument position:
// after the positional build context or service name a bare word would become
// another build context or another started service.
func TestBuildCommandPlacesArgumentsBeforePositionals(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{dockerfileName, "compose.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	composeFile := filepath.Join(dir, "compose.yaml")
	r := newTestRunner()
	stubCompose(t, r, nil, []string{"api"})
	tests := []struct {
		name string
		task runner.Task
		args []string
		want []string
	}{
		{
			name: "build",
			task: dockerfileTask(dir),
			args: []string{"--build-arg", "MODE=test"},
			want: []string{
				"docker", "build",
				"--file", filepath.Join(dir, dockerfileName),
				"--tag", imageReference(dir),
				"--build-arg", "MODE=test",
				".",
			},
		},
		{
			name: "compose service",
			task: composeTasks([]string{"api"})[2],
			args: []string{"--build"},
			want: []string{
				"docker", "compose", "--file", composeFile,
				"up", "--detach", "--build", "api",
			},
		},
		{
			name: "compose down",
			task: composeTasks(nil)[1],
			args: []string{"--volumes"},
			want: []string{
				"docker", "compose", "--file", composeFile,
				"down", "--volumes",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := r.BuildCommand(context.Background(), dir, test.task, test.args)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if got := cmd.Args; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("command args = %#v, want %#v", got, test.want)
			}
		})
	}
}

// TestListTasksReusesListingUntilComposeFilesChange keeps discovery from paying
// for a "compose config" process per project on every workspace scan. The
// fingerprint follows the file contents, so rewriting a file unchanged keeps the
// cached listing while a real edit drops it.
func TestListTasksReusesListingUntilComposeFilesChange(t *testing.T) {
	dir := t.TempDir()
	composeFile := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := newTestRunner()
	stub := stubCompose(t, r, nil, []string{"api"})

	for range 2 {
		if _, err := r.ListTasks(context.Background(), dir); err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
	}
	if len(stub.calls) != 2 {
		t.Fatalf("Docker calls = %d, want the 2 calls of one reused listing", len(stub.calls))
	}

	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ListTasks(context.Background(), dir); err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("Docker calls = %d, want no listing after an unchanged rewrite", len(stub.calls))
	}

	if err := os.WriteFile(composeFile, []byte("services: {api: {}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ListTasks(context.Background(), dir); err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(stub.calls) != 4 {
		t.Fatalf("Docker calls = %d, want a fresh listing after the edit", len(stub.calls))
	}
}

// TestListTasksReusesFailedListing keeps a project whose manifest cannot be
// listed from spawning a failing Docker process on every workspace scan.
func TestListTasksReusesFailedListing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "compose.yaml"),
		[]byte("services: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	r := newTestRunner()
	calls := 0
	r.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		calls++
		// #nosec G204 -- fixed missing-binary path used to exercise the error path.
		return exec.CommandContext(ctx, filepath.Join(dir, "absent-docker"))
	}

	for range 2 {
		if _, err := r.ListTasks(context.Background(), dir); err == nil {
			t.Fatal("ListTasks hid a failing Docker Compose listing")
		}
	}
	if calls != 1 {
		t.Fatalf("Docker calls = %d, want 1 reused failure", calls)
	}
}

// TestListTasksDoesNotCacheCancelledListing keeps a scan that was cut short
// from poisoning the cache. The cancellation says nothing about the manifest,
// so caching it would keep reporting a broken project until a Compose file
// happens to change.
func TestListTasksDoesNotCacheCancelledListing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "compose.yaml"),
		[]byte("services: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	r := newTestRunner()
	stubCompose(t, r, nil, []string{"api"})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ListTasks(cancelled, dir); err == nil {
		t.Fatal("ListTasks hid the cancelled Docker Compose listing")
	}

	// A cached cancellation would answer this call with the same failure and no
	// service task, on files that never changed.
	tasks, err := r.ListTasks(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListTasks after cancellation: %v", err)
	}
	if !slices.Contains(taskIDs(tasks), composeServiceID+"api") {
		t.Fatalf("tasks = %#v, want the listing repeated after the cancellation", taskIDs(tasks))
	}
}

// TestListTasksReportsMissingComposePlugin covers a host that has the Docker
// binary but not the Compose v2 plugin: the Dockerfile build stays runnable and
// the gap is a warning about the host, not a broken project.
func TestListTasksReportsMissingComposePlugin(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{dockerfileName, "compose.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := newTestRunner()
	helperBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// #nosec G204 -- fixed Go test binary and helper selector.
		cmd := exec.CommandContext(ctx, helperBinary, "-test.run=^TestDockerMissingPluginHelper$")
		cmd.Env = append(os.Environ(), "JMW_DOCKER_HELPER=missing-plugin")
		return cmd
	}

	tasks, err := r.ListTasks(context.Background(), dir)
	if !errors.Is(err, runner.ErrToolUnavailable) {
		t.Fatalf("ListTasks error = %v, want ErrToolUnavailable", err)
	}
	if want := []string{dockerBuildTaskID}; !reflect.DeepEqual(taskIDs(tasks), want) {
		t.Fatalf("tasks = %#v, want %#v", taskIDs(tasks), want)
	}
}

func TestDockerMissingPluginHelper(_ *testing.T) {
	if os.Getenv("JMW_DOCKER_HELPER") != "missing-plugin" {
		return
	}
	if _, err := os.Stderr.WriteString("docker: 'compose' is not a docker command.\n"); err != nil {
		os.Exit(1)
	}
	os.Exit(1)
}

// TestListTasksDoesNotCacheIncludedManifests covers "include": it pulls in files
// this package cannot enumerate, so their edits would not invalidate a cached
// listing and the listing must be repeated instead.
func TestListTasksDoesNotCacheIncludedManifests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "compose.yaml"),
		[]byte("include:\n  - other/compose.yaml\nservices: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	r := newTestRunner()
	stub := stubCompose(t, r, nil, []string{"api"})

	for range 2 {
		if _, err := r.ListTasks(context.Background(), dir); err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
	}
	if len(stub.calls) != 4 {
		t.Fatalf("Docker calls = %d, want a fresh listing per scan", len(stub.calls))
	}
}

// TestProjectCacheStaysBounded keeps a long-lived server from growing one cache
// entry per project directory it ever saw.
func TestProjectCacheStaysBounded(t *testing.T) {
	r := newTestRunner()
	root := t.TempDir()
	for index := range maxCachedProjects + 1 {
		r.projectCache(filepath.Join(root, "absent", strconv.Itoa(index)))
	}
	r.cachesMutex.Lock()
	defer r.cachesMutex.Unlock()
	if len(r.caches) > maxCachedProjects {
		t.Fatalf("cached projects = %d, want at most %d", len(r.caches), maxCachedProjects)
	}
}

func taskIDs(tasks []runner.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}
