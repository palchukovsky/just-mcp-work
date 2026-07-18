#!/usr/bin/env python3
"""Cross-platform development, build, package, release, and smoke helpers."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import queue
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BINARY = "just-mcp-work.exe" if os.name == "nt" else "just-mcp-work"
TARGETS = [
    ("linux", "amd64"),
    ("linux", "arm64"),
    ("darwin", "arm64"),
    ("windows", "amd64"),
]


def run(command: list[str], *, env: dict[str, str] | None = None, cwd: Path = ROOT) -> None:
    subprocess.run(command, cwd=cwd, env=env, check=True)


def ensure_venv(_: argparse.Namespace) -> None:
    venv = Path(os.environ.get("JMW_VENV", ROOT / ".venv"))
    python = venv / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
    if not python.exists():
        run([sys.executable, "-m", "venv", str(venv)])


def gofmt_check(_: argparse.Namespace) -> None:
    files = sorted(str(path.relative_to(ROOT)) for path in ROOT.rglob("*.go") if ".git" not in path.parts)
    completed = subprocess.run(["gofmt", "-l", *files], cwd=ROOT, check=True, text=True, capture_output=True)
    if completed.stdout.strip():
        raise SystemExit("Go files need formatting:\n" + completed.stdout)


def build(args: argparse.Namespace) -> None:
    version, commit = build_info()
    targets = TARGETS if args.all_targets else [(host_goos(), host_goarch())]
    for goos, goarch in targets:
        output = output_path(goos, goarch, args.all_targets)
        output.parent.mkdir(parents=True, exist_ok=True)
        environment = os.environ.copy()
        environment.update({"CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch})
        ldflags = (
            "-s -w -X github.com/palchukovsky/just-mcp-work/internal/version.Version=" + version
            + " -X github.com/palchukovsky/just-mcp-work/internal/version.Commit=" + commit
        )
        run(["go", "build", "-trimpath", "-ldflags", ldflags, "-o", str(output), "./cmd/just-mcp-work"], env=environment)


def package(_: argparse.Namespace) -> None:
    dist = ROOT / "dist"
    archives: list[Path] = []
    for goos, goarch in TARGETS:
        staged = output_path(goos, goarch, True)
        if not staged.exists():
            raise SystemExit(f"missing build output: {staged}")
        stem = f"just-mcp-work_{goos}_{goarch}"
        archive = dist / (f"{stem}.zip" if goos == "windows" else f"{stem}.tar.gz")
        if goos == "windows":
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as bundle:
                bundle.write(staged, arcname="just-mcp-work.exe")
        else:
            with tarfile.open(archive, "w:gz") as bundle:
                bundle.add(staged, arcname="just-mcp-work")
        archives.append(archive)
    checksums = [f"{sha256(path)}  {path.name}" for path in sorted(archives)]
    (dist / "checksums.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")


def smoke(_: argparse.Namespace) -> None:
    if shutil.which("just") is None:
        print("smoke: skipped (just is not installed)")
        return
    with tempfile.TemporaryDirectory(prefix="just-mcp-work-smoke-") as temporary:
        root = Path(temporary)
        (root / "justfile").write_text("hello:\n    @echo hello\n", encoding="utf-8")
        process = subprocess.Popen(
            ["go", "run", "./cmd/just-mcp-work", "serve", "--root", str(root)],
            cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1,
        )
        try:
            responses: queue.Queue[str] = queue.Queue()
            threading.Thread(target=drain_stdout, args=(process, responses), daemon=True).start()
            request(process, responses, 1, "initialize", {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "dev"}})
            send(process, {"jsonrpc": "2.0", "method": "notifications/initialized"})
            tools = request(process, responses, 2, "tools/list", {})
            tools_by_name = {tool["name"]: tool for tool in tools["result"]["tools"]}
            expected_tools = {
                "list_projects",
                "list_tasks",
                "run_task",
                "run_shell_command",
                "get_run",
                "get_run_logs",
                "version_status",
            }
            if missing := expected_tools - tools_by_name.keys():
                raise RuntimeError(f"server did not expose the expected tools: {missing!r}")
            assert_input_schema(
                tools_by_name["run_shell_command"],
                properties={"command", "working_directory"},
                required={"command"},
            )
            assert_input_schema(
                tools_by_name["version_status"],
                properties=set(),
                required=set(),
            )

            projects = call_tool(process, responses, 3, "list_projects", {})
            project_path = projects["projects"][0]["rel_path"]
            tasks = call_tool(process, responses, 4, "list_tasks", {"project_path": project_path})
            if not tasks["tasks"]:
                raise RuntimeError(f"server returned no tasks: projects={projects!r} tasks={tasks!r}")
            task_id = tasks["tasks"][0]["task_id"]
            receipt = call_tool(
                process,
                responses,
                5,
                "run_task",
                {"project_path": project_path, "task_id": task_id, "arguments": []},
            )
            if not receipt["ok"] or "hello" in json.dumps(receipt):
                raise RuntimeError("task receipt was not compact and successful")
            logs = call_tool(
                process,
                responses,
                6,
                "get_run_logs",
                {"run_id": receipt["run_id"], "stream": "stdout", "offset": 0, "limit": 64},
            )
            if logs["data"] != "hello\n":
                raise RuntimeError(f"unexpected task output: {logs['data']!r}")

            shell_marker = "just-mcp-work-shell-smoke"
            shell_receipt = call_tool(
                process,
                responses,
                7,
                "run_shell_command",
                {"command": "echo " + shell_marker, "working_directory": "."},
            )
            if not shell_receipt["ok"] or shell_marker in json.dumps(shell_receipt):
                raise RuntimeError("shell receipt was not compact and successful")
            shell_logs = call_tool(
                process,
                responses,
                8,
                "get_run_logs",
                {
                    "run_id": shell_receipt["run_id"],
                    "stream": "stdout",
                    "offset": 0,
                    "limit": 64,
                },
            )
            if shell_logs["data"].strip() != shell_marker:
                raise RuntimeError(f"unexpected shell output: {shell_logs['data']!r}")

            version_status = call_tool(process, responses, 9, "version_status", {})
            assert_typed_fields(
                "version_status",
                version_status,
                {"current_version": str, "update_available": bool, "message": str},
            )
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)


def assert_input_schema(tool: dict[str, object], *, properties: set[str], required: set[str]) -> None:
    schema = tool.get("inputSchema")
    if not isinstance(schema, dict) or schema.get("type") != "object":
        raise RuntimeError(f"{tool['name']} has an invalid input schema: {schema!r}")
    actual_properties = schema.get("properties", {})
    if not isinstance(actual_properties, dict) or properties - actual_properties.keys():
        raise RuntimeError(f"{tool['name']} input properties changed: {actual_properties!r}")
    actual_required = schema.get("required", [])
    if not isinstance(actual_required, list) or set(actual_required) != required:
        raise RuntimeError(f"{tool['name']} required inputs changed: {actual_required!r}")


def assert_typed_fields(name: str, payload: dict[str, object], fields: dict[str, type]) -> None:
    invalid = {
        field: payload.get(field)
        for field, expected_type in fields.items()
        if not isinstance(payload.get(field), expected_type)
    }
    if invalid:
        raise RuntimeError(f"{name} response fields changed: {invalid!r}")


def call_tool(
    process: subprocess.Popen[str],
    responses: queue.Queue[str],
    request_id: int,
    name: str,
    arguments: dict[str, object],
) -> dict[str, object]:
    return structured(
        request(
            process,
            responses,
            request_id,
            "tools/call",
            {"name": name, "arguments": arguments},
        )
    )


def release(args: argparse.Namespace) -> None:
    if args.kind not in {"patch", "minor", "major"}:
        raise SystemExit("release kind must be patch, minor, or major")
    ensure_clean_worktree()
    version = next_version(args.kind)
    if args.dry_run:
        run(
            [
                "gh",
                "workflow",
                "run",
                "release.yml",
                "--ref",
                current_branch(),
                "-f",
                "dry_run=true",
                "-f",
                "version=" + version,
            ],
        )
        print(f"release: started dry-run pipeline for {version}")
        return
    run(["git", "tag", version])
    run(["git", "push", "origin", version])
    print(f"release: pushed tag {version}; the release pipeline will build artifacts")


def ensure_clean_worktree() -> None:
    completed = subprocess.run(
        ["git", "status", "--porcelain"],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=True,
    )
    if completed.stdout:
        raise SystemExit("release requires a clean worktree")


def current_branch() -> str:
    completed = subprocess.run(
        ["git", "branch", "--show-current"],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=True,
    )
    branch = completed.stdout.strip()
    if not branch:
        raise SystemExit("release-dry requires a named branch")
    return branch


def run_binary(args: argparse.Namespace) -> None:
    binary = output_path(host_goos(), host_goarch(), False)
    if not binary.exists():
        raise SystemExit("build the binary first with: just build")
    run([str(binary), *args.command])


def request(process: subprocess.Popen[str], responses: queue.Queue[str], request_id: int, method: str, params: dict[str, object]) -> dict[str, object]:
    send(process, {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params})
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        try:
            line = responses.get(timeout=min(0.2, deadline - time.monotonic()))
        except queue.Empty:
            if process.poll() is not None:
                raise RuntimeError("server exited during smoke test")
            continue
        message = json.loads(line)
        if message.get("id") == request_id:
            return message
    raise RuntimeError(f"timed out waiting for {method}")


def drain_stdout(process: subprocess.Popen[str], responses: queue.Queue[str]) -> None:
    assert process.stdout is not None
    for line in process.stdout:
        responses.put(line)


def send(process: subprocess.Popen[str], message: dict[str, object]) -> None:
    assert process.stdin is not None
    process.stdin.write(json.dumps(message) + "\n")
    process.stdin.flush()


def structured(response: dict[str, object]) -> dict[str, object]:
    result = response["result"]
    assert isinstance(result, dict)
    content = result["structuredContent"]
    if isinstance(content, str):
        return json.loads(content)
    assert isinstance(content, dict)
    return content


def build_info() -> tuple[str, str]:
    version = os.environ.get("JMW_VERSION", "dev")
    completed = subprocess.run(["git", "rev-parse", "--short", "HEAD"], cwd=ROOT, text=True, capture_output=True)
    return version, completed.stdout.strip() if completed.returncode == 0 else "none"


def next_version(kind: str) -> str:
    completed = subprocess.run(
        ["git", "tag", "--list", "v*", "--sort=-version:refname"],
        cwd=ROOT,
        text=True,
        capture_output=True,
        check=True,
    )
    latest = completed.stdout.splitlines()[0] if completed.stdout.splitlines() else "v0.0.0"
    try:
        major, minor, patch = (int(component) for component in latest.removeprefix("v").split("."))
    except ValueError as error:
        raise SystemExit(f"latest version tag is not vMAJOR.MINOR.PATCH: {latest}") from error
    if kind == "major":
        major, minor, patch = major + 1, 0, 0
    elif kind == "minor":
        minor, patch = minor + 1, 0
    else:
        patch += 1
    return f"v{major}.{minor}.{patch}"


def output_path(goos: str, goarch: str, all_targets: bool) -> Path:
    if all_targets:
        name = "just-mcp-work.exe" if goos == "windows" else "just-mcp-work"
        return ROOT / "dist" / "build" / f"{goos}_{goarch}" / name
    return ROOT / "bin" / BINARY


def host_goos() -> str:
    return {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}[platform.system()]


def host_goarch() -> str:
    machine = platform.machine().lower()
    return {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[machine]


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("ensure-venv").set_defaults(func=ensure_venv)
    commands.add_parser("gofmt-check").set_defaults(func=gofmt_check)
    build_parser = commands.add_parser("build")
    build_parser.add_argument("--all-targets", action="store_true")
    build_parser.set_defaults(func=build)
    commands.add_parser("package").set_defaults(func=package)
    commands.add_parser("smoke").set_defaults(func=smoke)
    release_parser = commands.add_parser("release")
    release_parser.add_argument("kind")
    release_parser.add_argument("--dry-run", action="store_true")
    release_parser.set_defaults(func=release)
    run_parser = commands.add_parser("run")
    run_parser.add_argument("command", nargs=argparse.REMAINDER)
    run_parser.set_defaults(func=run_binary)
    return parser.parse_args()


if __name__ == "__main__":
    parsed = parse_args()
    parsed.func(parsed)
