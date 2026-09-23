# Set up a workspace

`just-mcp-work init` prepares one workspace. Run it there once:

```console
just-mcp-work init
```

It writes the managed instruction block for the selected agents, the MCP
configuration their clients read, the runner policy, and the generated agent
guide. Every question below has a flag that answers it in a scripted run;
`init --help` and `serve --help` list the agent targets and the server options.

## How `init` asks

When both standard input and standard error are a terminal with cursor control,
`init` shows every question at once as one form: the arrow keys move and change
answers, the space bar toggles, and nothing is written until the `Apply` row is
confirmed; `q` or `Esc` closes the form without writing. Under the form, the
focused question explains what it decides and lists its answers, the chosen
one with what it does and what it risks; `?` explains every answer. Each answer
starts as the recorded or default one, so pressing Enter through the form
answers what an empty answer to each question would. After the form closes,
the chosen answers stay in the terminal as plain lines.

Anywhere else `init` asks the same questions one at a time as text, answered
by typing: when input is piped or redirected, in a terminal that reports
`TERM=dumb`, in the classic Windows console window, and in Git Bash's mintty.
In the last three it first names a terminal that shows the form - Windows
Terminal on Windows. On Windows the form runs in a terminal that hosts the
console through a pseudo console, such as Windows Terminal or the VS Code
terminal.

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
neither verifies nor repairs them, and notices only a release that would write
a different block. In `--dry-run`, their diff contains only the managed block,
never personal text around it. `.mcp.json`,
`.codex/config.toml`, `.claude/settings.json`, `.just-mcp-work/`, and the
runner policy remain at the workspace scope for every target.

## How much the instruction block says

By default each selected agent instruction file carries the full managed
contract. Pass `--instructions-pointer` to write only its opening sentence,
which points at the MCP server's instructions. Choosing the shorter block
asserts that every selected client actually delivers those instructions to the
model; `init` cannot detect whether a client does.

The choice is recorded in the managed manifest, so a later `init` without the
flag keeps it. Pass `--instructions-pointer=true` or
`--instructions-pointer=false` to change it; in a fresh workspace, omitting the
flag selects the full block. On downgrade, an older `init` ignores the unknown
field and rewrites the full block, while an older `serve` reports that the
generated configuration changed since it was written.

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
`.just-mcp-work.json` in the workspace scope root, next to `.mcp.json`. The
questions name each runner after its tool - Golang, just, GNU Make, CMake,
Docker, and AI agent for `go`, `just`, `make`, `cmake`, `docker`, and `agent` -
and say what it runs; `--runner-mode` and the policy file use the short names.

[SECURITY.md](../SECURITY.md) describes what each mode exposes and what it does
not restrict. To change the selection, run `init`, not `serve --runner-mode`.

## Skipped directories

Project discovery always skips `.git` and `.just-mcp-work`; everything else it
skips is the workspace's own choice. `init` asks which directories that is:

- `none` - nothing else.
- `recommended` - the recommended directories found in this workspace.
- `custom` - only the directories you list.
- `both` - the recommended ones and your list.

`init` recommends a directory by name when it usually holds build output,
fetched or vendored dependencies, or a CI checkout - `build`, `builds`,
`_build`, `out`, `dist`, `distr`, `target`, `obj`, `_deps`, `node_modules`,
`vendor`, `external`, `third_party`, `thirdparty`, `3rdparty`, `venv` - and
only when such a directory exists in the workspace. Hidden directories and
the inside of a match are not searched, and a directory you may not read is
skipped with a notice naming it. Without any, `recommended` and `both` are
not offered. The question names what was found and where.

Your list is typed as comma-separated entries. A plain name skips every
directory so named anywhere in the workspace. An entry with a slash or a glob
character, such as `tools/*/out`, is matched against the whole path from the
workspace root, one path segment per `*`, so `cmake-build-*` skips only
top-level directories; it keeps that meaning when `serve --root` names a
subdirectory of the workspace. In the form, Enter on the list row starts
typing it, pasted lines become entries, and Enter again keeps it; `Esc` drops
the edit.

`init` writes the answer to `.just-mcp-work.json` as the recommended names it
took and your list, and a later run offers both again. A recorded recommended
name stays while its directory is absent - build output comes and goes - and a
name found since joins the recommended answer, which the question says. The
offer is `none` in a workspace that has none recorded. Pass `--exclude-mode
none|recommended|custom|both` and, with `custom` or `both`,
`--exclude <entry>,...` to answer non-interactively; `none` and `custom` skip
the search for recommendations. A policy written before this question existed
skips nothing beyond `.git` and `.just-mcp-work` until `init` runs again.
`serve --exclude` adds entries for one server on top of the policy, matched
from the directory `serve` runs on.

## AI families

`init` also asks which AI families the managed server should declare. The
question lists its choices with numbers, each with the agent it is for, and the
answer names as many as the workspace needs, by number or by name, separated
by commas or spaces. Both `codex` and `claude` are offered in a workspace that
has none recorded. Each family is declared in the configuration its own agent
reads - `claude` in `.mcp.json` for Claude Code, `codex` in
`.codex/config.toml` for Codex - so a configuration whose family was not
selected declares none. Cursor, GitHub Copilot, and Windsurf have no family:
`init` writes only their instruction files, not an MCP configuration. Later
runs offer the families stored by the previous `init`; when the recorded ones
are not recognized, as after an upgrade
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

`init` asks whether the workspace takes part in the JMW beta test; pass
`--beta-test` or `--beta-test=false` to answer it in a scripted run. Taking
part records beta-test mode, so the selected agents and every client
connecting to the JMW server receive beta feedback guidance. With
`--instructions-pointer`, the instruction files carry the opening sentence of
that guidance as a pointer to the server's instructions rather than its full
text.

A later `init` offers the recorded answer, so accepting it keeps the workspace
in or out of the beta test, and answering the other way changes it. `init`
reads every recorded answer before its first question, so a manifest that
cannot be read - malformed, or of an unsupported schema - stops it there
unless `--ai` answers the AI families; with `--ai`, the beta question says the
recorded mode cannot be used and offers `no`, and a malformed manifest is
still refused when `init` plans its writes. Input that ends before the question
is answered stops `init` before it writes. The separate `init-beta-test`
command of earlier releases is gone; `init --beta-test` replaces it.

## After an update

An update does not require `init` on its own. At startup, `serve` checks every
surface recorded in the workspace against what the installed binary would
generate now, so a release that changes none of them starts without any
further step. When a release does change one, `serve` refuses to start and
names that file; the refusal, not the update, is what asks for `init`. A
workspace initialized before the managed manifest existed has no record to
check against, so run `init` there once to gain one. A machine instruction
file is not a recorded surface, so `serve` never reads it; the manifest
records the digest of the block instead, and a release that would write a
different block is refused the same way.
