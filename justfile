set dotenv-load
set dotenv-path := ".github/ci-versions.env"
set windows-shell := ["powershell.exe", "-NoLogo", "-Command"]

repo_dir := justfile_directory()
go_version := env_var_or_default("CI_GO", "1.25.11")
golangci_lint_version := env_var_or_default("CI_GOLANGCI_LINT", "2.12.2")
w64devkit_version := env_var("CI_W64DEVKIT")
w64devkit_sha256 := env_var("CI_W64DEVKIT_SHA256")
local_tool_dir := repo_dir / ".tmp" / "bin"
default_golangci_lint := if os_family() == "windows" { local_tool_dir / "golangci-lint.exe" } else { local_tool_dir / "golangci-lint" }
golangci_lint := env_var_or_default("GOLANGCI_LINT", default_golangci_lint)
go_cache := env_var_or_default("GOCACHE", repo_dir / ".tmp" / "go-build-cache")
golangci_lint_cache := env_var_or_default("GOLANGCI_LINT_CACHE", repo_dir / ".tmp" / "golangci-lint-cache")
python := if os_family() == "windows" { "python" } else { "python3" }
venv_dir := repo_dir / ".venv"
venv_python := if os_family() == "windows" { venv_dir / "Scripts" / "python.exe" } else { venv_dir / "bin" / "python" }
semgrep := if os_family() == "windows" { venv_dir / "Scripts" / "semgrep.exe" } else { venv_dir / "bin" / "semgrep" }
w64devkit_dir := repo_dir / ".tmp" / ("w64devkit-" + w64devkit_version)
w64devkit_bin := w64devkit_dir / "w64devkit" / "bin"
windows_race_environment := if os_family() == "windows" { "$env:COMPILER_PATH='" + w64devkit_bin + "'; $env:CGO_ENABLED='1'; $env:CC='" + w64devkit_bin / "gcc.exe" + "';" } else { "" }
export GOCACHE := go_cache
export GOLANGCI_LINT_CACHE := golangci_lint_cache
export GOBIN := local_tool_dir

# Show available recipes.
default:
    @just --list --unsorted

# Print the selected toolchain versions.
versions:
    @go version
    @echo "configured Go: {{ go_version }}"
    @{{ golangci_lint }} version
    @echo "configured golangci-lint: {{ golangci_lint_version }}"

# Build every package in the module.
build:
    go build ./...

# Format Go files in place using the configured golangci formatters.
fmt:
    {{ golangci_lint }} fmt

# Check formatting without changing files.
fmt-check:
    {{ golangci_lint }} fmt --diff

# Run the standard Go static analyzer with every vet pass enabled.
vet:
    go vet -all ./...

# Run the configured strict linter set.
lint:
    {{ golangci_lint }} run --timeout=5m ./...

# Run unit and package tests once.
test:
    go test -count=1 ./...

# Run tests with the race detector.
test-race:
    {{ windows_race_environment }} go test -race -count=1 ./...

# Produce a coverage profile and human-readable summary.
test-cover:
    go test -covermode=atomic -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out

# Verify that go.mod and go.sum are tidy without changing them.
tidy-check:
    go mod tidy -diff

# Update module metadata explicitly when dependencies change.
tidy:
    go mod tidy

# Run the non-mutating checks after formatting and dependency metadata checks.
check: fmt-check tidy-check check-dry

# Run the non-mutating build, lint, and test checks.
check-dry: lint vet test test-race build check-semgrep

# Run the project-specific invariant rule set.
check-semgrep:
    {{ semgrep }} --config checks/semgrep/ --error --quiet .

# Run all project checks, including the MCP smoke test.
verify: check
    {{ python }} scripts/dev.py smoke

# Install the pinned golangci-lint binary into the ignored repo-local .tmp/bin.
install-lint:
    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v{{ golangci_lint_version }}

# Install the pinned Semgrep binary into the ignored repo-local virtual environment.
install-semgrep:
    {{ python }} -m venv {{ venv_dir }}
    {{ venv_python }} -m pip install --requirement checks/requirements.txt

# Install the pinned Windows C toolchain required by the Go race detector.
[windows]
install-race-toolchain:
    powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File scripts/install-race-toolchain.ps1 -Version {{ w64devkit_version }} -Sha256 {{ w64devkit_sha256 }} -Destination {{ w64devkit_dir }}

# Install every tool required by the Windows check and verify recipes.
[windows]
install-windows-tools: install-lint install-semgrep install-race-toolchain

# Scan dependencies and source for known Go vulnerabilities.
vuln:
    go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Build release binaries for all supported targets.
build-all:
    {{ python }} scripts/dev.py build --all-targets

# Package release archives and checksums.
package: build-all
    {{ python }} scripts/dev.py package

# Verify, create, and push the next semantic-version tag.
release kind: verify
    {{ python }} scripts/dev.py release {{ kind }}

# Run the release pipeline for a prospective version without creating a tag.
release-dry kind="patch":
    {{ python }} scripts/dev.py release {{ kind }} --dry-run
