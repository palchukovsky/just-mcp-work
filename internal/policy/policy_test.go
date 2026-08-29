// Copyright (c) Eugene V. Palchukovsky
// SPDX-License-Identifier: MIT
// Please see https://github.com/palchukovsky/just-mcp-work for details.

package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/palchukovsky/just-mcp-work/internal/runner"
)

func TestPath(t *testing.T) {
	root := filepath.Join("workspace", "scope")
	want := filepath.Join(root, ".just-mcp-work.json")
	if got := Path(root); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestLoadAbsentPolicy(t *testing.T) {
	loaded, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Found {
		t.Fatal("Load() Found = true, want false")
	}
	if len(loaded.Selections) != 0 {
		t.Fatalf("Load() selections = %v, want empty", loaded.Selections)
	}
}

func TestSaveLoadRoundTripPreservesOrder(t *testing.T) {
	root := t.TempDir()
	var expectedMode os.FileMode
	if runtime.GOOS != "windows" {
		probePath := filepath.Join(root, "mode-probe")
		// #nosec G306 -- The probe records the process umask applied to a 0644 creation.
		if err := os.WriteFile(probePath, nil, 0o644); err != nil {
			t.Fatalf("write mode probe: %v", err)
		}
		probeInfo, err := os.Stat(probePath)
		if err != nil {
			t.Fatalf("stat mode probe: %v", err)
		}
		expectedMode = probeInfo.Mode().Perm()
	}
	validated := validatedSelections(
		t,
		[]string{"just", "go", "make"},
		[]runner.Selection{{Name: "go", Mode: runner.ModeDisabled}},
	)
	if err := Save(root, validated); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(Path(root))
		if err != nil {
			t.Fatalf("stat saved policy: %v", err)
		}
		if got := info.Mode().Perm(); got != expectedMode {
			t.Fatalf("saved policy mode = %o, want umask-filtered %o", got, expectedMode)
		}
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.Found {
		t.Fatal("Load() Found = false, want true")
	}
	want, err := validated.Selections()
	if err != nil {
		t.Fatalf("validated selections: %v", err)
	}
	if !slices.Equal(loaded.Selections, want) {
		t.Fatalf("Load() selections = %v, want %v", loaded.Selections, want)
	}
}

func TestLoadEmptyPolicy(t *testing.T) {
	root := t.TempDir()
	writePolicyFile(t, root, `{"version":1,"runners":[]}`)
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.Found {
		t.Fatal("Load() Found = false, want true")
	}
	if len(loaded.Selections) != 0 {
		t.Fatalf("Load() selections = %v, want empty", loaded.Selections)
	}
}

func TestSaveLoadRoundTripEmptyCatalog(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, validatedSelections(t, nil, nil)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.Found || len(loaded.Selections) != 0 {
		t.Fatalf("Load() = %+v, want found policy with no selections", loaded)
	}
}

func TestSaveWritesStableDocument(t *testing.T) {
	root := t.TempDir()
	validated := validatedSelections(
		t,
		[]string{"go"},
		[]runner.Selection{{Name: "go", Mode: runner.ModeDisabled}},
	)
	if err := Save(root, validated); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatalf("read saved policy: %v", err)
	}
	want := "{\n" +
		"  \"version\": 1,\n" +
		"  \"runners\": [\n" +
		"    {\n" +
		"      \"name\": \"go\",\n" +
		"      \"mode\": \"disabled\"\n" +
		"    }\n" +
		"  ]\n" +
		"}\n"
	if string(data) != want {
		t.Fatalf("saved policy = %q, want %q", data, want)
	}
}

func TestEncodeMatchesSave(t *testing.T) {
	root := t.TempDir()
	validated := validatedSelections(
		t,
		[]string{"just", "go"},
		[]runner.Selection{{Name: "go", Mode: runner.ModeDisabled}},
	)
	encoded, err := Encode(validated)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if saveErr := Save(root, validated); saveErr != nil {
		t.Fatalf("Save() error = %v", saveErr)
	}
	saved, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatalf("read saved policy: %v", err)
	}
	if !slices.Equal(encoded, saved) {
		t.Fatalf("Encode() = %q, saved policy = %q", encoded, saved)
	}
}

func TestLoadRejectsMalformedPolicy(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "invalid JSON", content: `{"version":`, want: "decode JSON"},
		{
			name:    "unknown version",
			content: `{"version":2,"runners":[]}`,
			want:    "unsupported version 2",
		},
		{
			name:    "missing version",
			content: `{"runners":[]}`,
			want:    "version is missing",
		},
		{
			name: "duplicate runner name",
			content: `{"version":1,"runners":[` +
				`{"name":"go","mode":"safe"},{"name":"go","mode":"all"}]}`,
			want: `runners[1].name "go" is duplicated`,
		},
		{
			name:    "empty name",
			content: `{"version":1,"runners":[{"name":"","mode":"safe"}]}`,
			want:    "runners[0].name is empty",
		},
		{
			name:    "missing name",
			content: `{"version":1,"runners":[{"mode":"safe"}]}`,
			want:    "runners[0].name is empty",
		},
		{
			name:    "empty mode",
			content: `{"version":1,"runners":[{"name":"go","mode":""}]}`,
			want:    "runners[0].mode is empty",
		},
		{
			name:    "missing mode",
			content: `{"version":1,"runners":[{"name":"go"}]}`,
			want:    "runners[0].mode is empty",
		},
		{
			name:    "unknown JSON member",
			content: `{"version":1,"runnerz":[]}`,
			want:    `unknown JSON member "runnerz"`,
		},
		{
			name: "case-variant entry member after exact member",
			content: `{"version":1,"runners":[` +
				`{"name":"go","mode":"safe","Mode":"all"}]}`,
			want: `runners[0] has unknown JSON member "Mode"`,
		},
		{
			name:    "case-variant entry member",
			content: `{"version":1,"runners":[{"name":"go","Mode":"all"}]}`,
			want:    `runners[0] has unknown JSON member "Mode"`,
		},
		{
			name: "repeated entry member",
			content: `{"version":1,"runners":[` +
				`{"name":"go","mode":"disabled","mode":"all"}]}`,
			want: `runners[0] JSON member "mode" is repeated`,
		},
		{
			name: "repeated root member",
			content: `{"version":1,"runners":[` +
				`{"name":"go","mode":"disabled"}],` +
				`"runners":[{"name":"go","mode":"all"}]}`,
			want: `JSON member "runners" is repeated`,
		},
		{
			name:    "case-variant root members",
			content: `{"Version":1,"Runners":[]}`,
			want:    `unknown JSON member "Version"`,
		},
		{
			name:    "missing runners",
			content: `{"version":1}`,
			want:    "runners must be an array",
		},
		{
			name:    "second top-level value",
			content: `{"version":1,"runners":[]} {}`,
			want:    "multiple top-level values",
		},
		{
			name:    "garbage after top-level value",
			content: `{"version":1,"runners":[]} garbage`,
			want:    "decode trailing JSON",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writePolicyFile(t, root, test.content)
			_, err := Load(root)
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), Path(root)) {
				t.Fatalf("Load() error = %q, want policy path", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestLoadAcceptsExactJSONMembers(t *testing.T) {
	root := t.TempDir()
	writePolicyFile(
		t,
		root,
		`{"version":1,"runners":[{"name":"go","mode":"safe"}]}`,
	)
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []runner.Selection{{Name: "go", Mode: runner.ModeSafe}}
	if !loaded.Found || !slices.Equal(loaded.Selections, want) {
		t.Fatalf("Load() = %+v, want found policy with selections %v", loaded, want)
	}
}

func TestLoadDefersSemanticValidationToRunnerCatalog(t *testing.T) {
	root := t.TempDir()
	writePolicyFile(
		t,
		root,
		`{"version":1,"runners":[{"name":"future-runner","mode":"future-mode"}]}`,
	)
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []runner.Selection{{Name: "future-runner", Mode: runner.Mode("future-mode")}}
	if !loaded.Found || !slices.Equal(loaded.Selections, want) {
		t.Fatalf("Load() = %+v, want found policy with selections %v", loaded, want)
	}
}

func TestLoadRejectsDanglingPolicySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	root := t.TempDir()
	path := Path(root)
	if err := os.Symlink(filepath.Join(root, "missing-policy"), path); err != nil {
		t.Fatalf("create dangling policy symlink: %v", err)
	}
	loaded, err := Load(root)
	if err == nil {
		t.Fatalf("Load() = %+v, nil error; want a hard error", loaded)
	}
	if !strings.Contains(err.Error(), path) ||
		!strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Load() error = %q, want path and symbolic-link type", err)
	}
}

func TestLoadReportsUnreadablePolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(Path(root), 0o750); err != nil {
		t.Fatalf("create directory at policy path: %v", err)
	}
	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), Path(root)) ||
		!strings.Contains(err.Error(), "directory") {
		t.Fatalf("Load() error = %q, want path and actual file type", err)
	}
}

func TestLoadAndSaveRejectEmptyRoot(t *testing.T) {
	validated := validatedSelections(t, nil, nil)
	tests := []struct {
		run  func() error
		name string
	}{
		{
			name: "load",
			run: func() error {
				_, err := Load("")
				if err != nil {
					return fmt.Errorf("load empty root: %w", err)
				}
				return nil
			},
		},
		{name: "save", run: func() error { return Save("", validated) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if err == nil {
				t.Fatal("error = nil, want an error")
			}
			if !strings.Contains(err.Error(), Path("")) ||
				!strings.Contains(err.Error(), "root must not be empty") {
				t.Fatalf("error = %q, want policy path and empty root", err)
			}
		})
	}
}

func TestLoadRejectsInvalidRoot(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) string
		want  string
	}{
		{
			name: "non-existent root",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing")
			},
			want: "inspect workspace root",
		},
		{
			name: "root is a file",
			setup: func(t *testing.T) string {
				root := filepath.Join(t.TempDir(), "workspace-file")
				if err := os.WriteFile(root, nil, 0o600); err != nil {
					t.Fatalf("write root fixture: %v", err)
				}
				return root
			},
			want: "workspace root is not a directory",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := test.setup(t)
			_, err := Load(root)
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), Path(root)) ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %q, want path and %q", err, test.want)
			}
		})
	}
}

func TestSaveRejectsUnvalidatedSelections(t *testing.T) {
	root := t.TempDir()
	err := Save(root, runner.ValidatedSelections{})
	if err == nil {
		t.Fatal("Save() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), Path(root)) ||
		!strings.Contains(err.Error(), "not validated by a catalog") {
		t.Fatalf("Save() error = %q, want path and validation failure", err)
	}
	if _, statErr := os.Stat(Path(root)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stat unwritten policy error = %v, want not exist", statErr)
	}
}

func TestSaveRejectsPolicySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target-policy")
	const original = "unchanged target"
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	path := Path(root)
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create policy symlink: %v", err)
	}
	err := Save(root, validatedSelections(t, nil, nil))
	if err == nil {
		t.Fatal("Save() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), path) ||
		!strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Save() error = %q, want path and symbolic-link type", err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatalf("lstat policy symlink: %v", statErr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("policy mode = %s, want symlink", info.Mode())
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read symlink target: %v", readErr)
	}
	if string(data) != original {
		t.Fatalf("symlink target = %q, want %q", data, original)
	}
}

func TestSaveOverwritesExistingPolicyAndPreservesMode(t *testing.T) {
	root := t.TempDir()
	first := validatedSelections(
		t,
		[]string{"go"},
		[]runner.Selection{{Name: "go", Mode: runner.ModeDisabled}},
	)
	if err := Save(root, first); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(Path(root), 0o600); err != nil {
			t.Fatalf("restrict existing policy mode: %v", err)
		}
	}
	second := validatedSelections(
		t,
		[]string{"go"},
		[]runner.Selection{{Name: "go", Mode: runner.ModeAll}},
	)
	if err := Save(root, second); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want, err := second.Selections()
	if err != nil {
		t.Fatalf("second selections: %v", err)
	}
	if !loaded.Found || !slices.Equal(loaded.Selections, want) {
		t.Fatalf("Load() = %+v, want found policy with selections %v", loaded, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(Path(root))
		if err != nil {
			t.Fatalf("stat overwritten policy: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("overwritten policy mode = %o, want 600", got)
		}
	}
}

func TestSavePreservesExistingPolicyOnPublishFailure(t *testing.T) {
	root := t.TempDir()
	first := validatedSelections(
		t,
		[]string{"go"},
		[]runner.Selection{{Name: "go", Mode: runner.ModeDisabled}},
	)
	if err := Save(root, first); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	before, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatalf("read first policy: %v", err)
	}
	second := validatedSelections(
		t,
		[]string{"go"},
		[]runner.Selection{{Name: "go", Mode: runner.ModeAll}},
	)
	publishErr := errors.New("publish denied")
	err = save(
		root,
		second,
		func(source string, destination string) error {
			if destination != Path(root) {
				t.Errorf("publish destination = %q, want %q", destination, Path(root))
			}
			if _, statErr := os.Stat(source); statErr != nil {
				t.Errorf("stat temporary file before publish: %v", statErr)
			}
			return publishErr
		},
	)
	if !errors.Is(err, publishErr) {
		t.Fatalf("Save() error = %v, want publish error", err)
	}
	after, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatalf("read policy after failed publish: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("policy after failed publish = %q, want %q", after, before)
	}
}

func TestSaveReportsUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions are not enforced on Windows")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("make directory unwritable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Errorf("restore directory permissions: %v", err)
		}
	})
	probe, probeErr := os.CreateTemp(root, "write-probe-*")
	if probeErr == nil {
		probeName := probe.Name()
		if err := probe.Close(); err != nil {
			t.Fatalf("close write probe: %v", err)
		}
		if err := os.Remove(probeName); err != nil {
			t.Fatalf("remove write probe: %v", err)
		}
		t.Skip("current user can write to permission-restricted directories")
	}
	err := Save(root, validatedSelections(t, nil, nil))
	if err == nil {
		t.Fatal("Save() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), Path(root)) ||
		!strings.Contains(err.Error(), "create temporary file") {
		t.Fatalf("Save() error = %q, want path and write failure", err)
	}
}

func validatedSelections(
	t *testing.T,
	names []string,
	overrides []runner.Selection,
) runner.ValidatedSelections {
	t.Helper()
	registrations := make([]runner.Registration, 0, len(names))
	for _, name := range names {
		registrations = append(registrations, runner.NewRegistration(
			name,
			runner.UnreviewedPermissions(),
			func(runner.Mode) (runner.Runner, error) {
				return nil, runner.ErrToolUnavailable
			},
		))
	}
	catalog, err := runner.NewCatalog(registrations...)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	validated, err := catalog.CanonicalSelections(overrides)
	if err != nil {
		t.Fatalf("CanonicalSelections() error = %v", err)
	}
	return validated
}

func writePolicyFile(t *testing.T, root string, content string) {
	t.Helper()
	if err := os.WriteFile(Path(root), []byte(content), 0o600); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}
}
