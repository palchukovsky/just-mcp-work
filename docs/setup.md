# Set up a workspace

`just-mcp-work init` prepares one workspace. Run it there once:

```console
just-mcp-work init
```

or, in a workspace beta-testing JMW:

```console
just-mcp-work init-beta-test
```

It writes the managed instruction block for the selected agents, the MCP
configuration their clients read, the runner policy, and the generated agent
guide. Every question below has a flag that answers it in a scripted run;
`init --help` and `serve --help` list the agent targets and the server options.

## Where the instruction block goes

`init` asks where to put its managed instruction block: `project` means the
directory named by `--dir`, `workspace` means the resolved workspace scope, and
`machine` means the agent's machine-wide file. Pass
`--instructions-target project|workspace|machine` to answer without a prompt.
`workspace` is the default and the previous behavior; a later run offers the
target recorded by the previous `init`.

The machine target supports Claude Code (`~/.claude/CLAUDE.md`), Codex
(`~/.codex/AGENTS.md`), and Windsurf
(`~/.codeium/windsurf/memories/global_rules.md`). The default `--agents
claude,codex,cursor` is therefore not valid with `machine`: select only
supported agents, for example `--agents claude,codex,windsurf`. The CLI checks
this immediately after the target is selected, before asking its later
questions, and never silently drops an agent.

The home and each machine file's parent directory are resolved before planning.
The resolved parent must remain inside the resolved home; a dotfiles link that
stays there works, while an escaping link is refused with both its lexical and
resolved paths. The leaf itself must be a regular file, not a symlink. A
missing leaf is created with the managed block; an existing regular file has
only the block between its markers replaced and is refused when the markers
are absent or malformed. Machine files are outside the workspace: `serve`
neither verifies nor repairs them. In `--dry-run`, their diff contains only
the managed block, never personal text around it. `.mcp.json`,
`.codex/config.toml`, `.claude/settings.json`, `.just-mcp-work/`, and the
runner policy remain at the workspace scope for every target.

## What `init` manages

Each invocation is authoritative inside the workspace scope resolved from
`--dir`, for the surfaces it manages. It adds the canonical instruction block
for the selected agents - Claude Code, Codex, Cursor, Copilot, and Windsurf -
and never reads the contents of or changes the instruction file of an agent
that is not selected. Its recorded surface remains in the manifest so
`serve` continues verifying it and a later run that reselects the agent can
clean an old destination. `.claude/settings.json` is touched only when
`claude` is one of the selected agents, and then follows the permission answer.
`.mcp.json` and `.codex/config.toml` follow `--write-mcp-config` rather than
`--agents`: they are rewritten when it is true and stripped of their JMW
entries when it is false, whether or not `codex` was selected. Two selected
agent targets that resolve to one document are accepted only when both edits
produce identical content; otherwise `init` refuses before writing and names
both surfaces and their shared path. A deselected aliased target remains
untouched. If moving a selected agent would remove a block from a path also
recorded for an unselected agent, `init` refuses and gives the `--agents` value
that makes the move safe.

It also writes `.just-mcp-work/guide.txt`, a generated reference for agents
working with JMW. Once `serve` verifies it, the short MCP instructions point to
its absolute path; without a verified guide, they contain the full reference.

A file left holding nothing but JMW state is removed, which can happen to
`.mcp.json`, `.codex/config.toml`, and `.claude/settings.json`, except that a
JMW-only `.mcp.json` scope anchor is kept as an empty object whenever deleting
it would change the scope of an identical repeated `init`. Instruction files
are never removed: an agent dropped from `--agents` keeps the block an earlier
run wrote for it and goes on obeying it, so delete that block by hand once the
agent should stop. A selected agent is different when its target changes: its
previous project or workspace file loses the managed block but remains. A
previous machine file is reported and left alone because it can be shared by
other workspaces on that machine; the advisory is limited to agents this
workspace's manifest says it wrote.

Every target this invocation manages is planned before any of them is written,
so a failure on a later target cannot leave an earlier one already changed.
Existing foreign entries and text are edited in place without broad
reformatting: their content, ordering, formatting, and line endings are
preserved. When removing an appended managed block, `init` also removes its
separator from a newline-terminated foreign file; a legacy file that had no
final newline is kept as valid text with one final line break because the
previous state is no longer distinguishable. `init` stops when it cannot edit a
target safely or finds a hand-written Codex entry for this server; it tells you
what to fix instead of taking the entry over. It never searches for instruction
or agent configuration targets above the resolved workspace scope.

## Runner modes

Every runner must register a permission declaration before it can enter the
runtime catalog. `init` asks about every declared runner; Go defaults to
`safe`, as does the agent runner. Just, Make, CMake, and Docker are currently
unreviewed and offer their existing `all` behavior by default or `disabled` for
compatibility while their command surfaces are reviewed separately. Pass the
repeatable `init --runner-mode <name>=<mode>` option to answer selected runner
questions non-interactively. `init` writes the complete canonical selection to
`.just-mcp-work.json` in the workspace scope root, next to `.mcp.json`.

[SECURITY.md](../SECURITY.md) describes what each mode exposes and what it does
not restrict. To change the selection, run `init`, not `serve --runner-mode`.

## AI families

`init` also asks which AI families the managed server should declare. The
question lists its choices with numbers, and the answer names as many as the
workspace needs, by number or by name, separated by commas or spaces. Both
`codex` and `claude` are offered in a workspace that has none recorded. Each
family is declared in the configuration its own client reads - `claude` in
`.mcp.json`, `codex` in `.codex/config.toml` - so a configuration whose family
was not selected declares none. Later runs offer the families stored by the
previous `init`; when the recorded ones are not recognized, as after an upgrade
from a release that stored a single family, `init` says so and asks again.
Until that `init` rewrites the manifest, `serve` refuses to start on it. Pass
`--ai codex|claude|codex,claude` to answer the question non-interactively. The
selection is recorded in the managed manifest. Generated arguments always
include `serve --root <dir>` and add `--ai codex|claude` when the family of
that configuration is declared. The profile is caller-declared presentation and
provenance; it does not change runner modes, task visibility, shell permission,
or write access.

## Shell tool permission

`init` also asks whether the shell tools may run without a client confirmation.
`ask` is the answer offered in a workspace that has none recorded; a later
`init` offers the recorded one instead. The answer goes to
`.claude/settings.json` and the managed block in `.codex/config.toml` when they
are managed. Pass `--shell-permission allow|ask` to answer it up front in a
scripted run. Changing this answer with a narrower `--agents` selection is
blocked only by recorded Claude settings outside that selection, not by machine
instruction files whose bytes do not depend on the shell permission.

## Beta-test mode

Use `init-beta-test` for a workspace beta-testing JMW. It does everything
`init` does and records beta-test mode, so the selected agents and every client
connecting to the JMW server receive beta feedback guidance.

In such a workspace, use `init-beta-test` again to stay in beta mode; plain
`init` asks before it removes beta feedback guidance and leaves beta testing.
Reaching end of input answers that question as yes, so plain `init` continues
to its preflight and leaves the beta test when that preflight succeeds.

## After an update

An update does not require `init` on its own. At startup, `serve` checks every
surface recorded in the workspace against what the installed binary would
generate now, so a release that changes none of them starts without any
further step. When a release does change one, `serve` refuses to start and
names that file; the refusal, not the update, is what asks for `init`. A
workspace initialized before the managed manifest existed has no record to
check against, so run `init` there once to gain one. A machine instruction
file is not a recorded surface either, so nothing detects a release that
changes the block in it; run `init` after an update when the block lives
there.
