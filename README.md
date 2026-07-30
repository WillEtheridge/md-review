# mdReview

mdReview is a local terminal application for review of Markdown from coding agents. It shows the
Markdown in a browser and saves comments in a JSON sidecar:

```text
plan.md
plan.md.review.json
```

mdReview does not change the Markdown, start an agent, or send comments to an agent. Give the
sidecar to your agent when the review is complete.

![The mdReview interface with files, a Markdown document, and comments](web/tests/visual/m4-1440.visual.spec.ts-snapshots/rich-discussion-light-chromium-1440-linux.png)

mdReview supports Linux on `amd64` and macOS on Apple silicon (`arm64`). It does not support Windows
or Intel Mac computers.

## Install

Open the [GitHub Releases page](https://github.com/WillEtheridge/md-review/releases). Download the
archive for your operating system.

| Operating system | Archive                                         |
| ---------------- | ----------------------------------------------- |
| Linux `amd64`    | `mdreview-v0.2.0-preview.1-linux-amd64.tar.gz`  |
| macOS `arm64`    | `mdreview-v0.2.0-preview.1-darwin-arm64.tar.gz` |

### Linux

Run these commands in the directory that contains the archive:

```bash
tar -xzf mdreview-v0.2.0-preview.1-linux-amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 0755 \
  mdreview-v0.2.0-preview.1-linux-amd64/mdreview \
  "$HOME/.local/bin/mdreview"
"$HOME/.local/bin/mdreview" --version
```

If the shell cannot find `mdreview`, add the binary directory to `PATH`:

```bash
printf '\nexport PATH="$HOME/.local/bin:$PATH"\n' >>"$HOME/.bashrc"
source "$HOME/.bashrc"
```

### Apple silicon macOS

Make sure that the Mac has Apple silicon:

```bash
uname -m
```

Continue if the output is `arm64`. Run these commands in the directory that contains the archive:

```bash
tar -xzf mdreview-v0.2.0-preview.1-darwin-arm64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 0755 \
  mdreview-v0.2.0-preview.1-darwin-arm64/mdreview \
  "$HOME/.local/bin/mdreview"
printf '\nexport PATH="$HOME/.local/bin:$PATH"\n' >>"$HOME/.zprofile"
source "$HOME/.zprofile"
mdreview --version
```

The macOS archive is not signed or notarised. If macOS blocks the first start:

1. Open **System Settings**.
2. Select **Privacy & Security**.
3. Find the message about `mdreview`.
4. Select **Open Anyway**.
5. Start `mdreview` again.

### Optional Agent Skill

Install the Agent Skill if you want an agent to find and address review comments. mdReview works
without the skill.

Run:

```bash
mdreview setup
```

Use the Up and Down keys to move. Press Space to select or clear an agent. Press Enter to install
the skill for the selected agents. Codex, Claude Code, and Pi are separate choices.

The installation is global for your user account. It is not local to the current project.

| Agent       | Global user file                            |
| ----------- | ------------------------------------------- |
| Codex       | `$HOME/.codex/skills/mdreview/SKILL.md`     |
| Claude Code | `$HOME/.claude/skills/mdreview/SKILL.md`    |
| Pi          | `$HOME/.pi/agent/skills/mdreview/SKILL.md`  |

For a non-interactive installation, specify each target:

```bash
mdreview skill install --target codex
mdreview skill install --target claude
mdreview skill install --target pi
```

## Use

Run mdReview in a project directory:

```bash
cd /path/to/project
mdreview .
```

mdReview runs in the foreground and prints a local URL:

```text
mdReview

Directory: /path/to/project
Documents: 14
URL:       http://127.0.0.1:4242/

Waiting for a browser connection. Press Ctrl+C to stop.
```

Open the printed URL in a browser. mdReview does not open the browser. Press `Ctrl+C` in the
terminal to stop mdReview.

With no directory argument, mdReview serves the current directory:

```bash
mdreview
mdreview /path/to/project
mdreview --port 4800 /path/to/project
```

mdReview first tries port `4242`. If that port is busy, it selects another port. An explicit busy
port is an error. Each invocation is an independent foreground process.

### Review workflow

1. Select a Markdown file in the file tree.
2. Select text, or add a document comment.
3. Add comments and replies.
4. Mark addressed work as handled.
5. Mark accepted work as resolved.
6. Commit or share the adjacent `.md.review.json` sidecar.

A thread can be `open`, `handled`, or `resolved`. You can delete a thread only before it has a
reply. Human messages are append-only. Add a reply if you must correct a comment.

mdReview follows the system theme initially. Use the Light or Dark control to override it. Resolved
threads are hidden by default and remain available through the filter.

The browser polls once per second while its tab is active. It notices changes to Markdown files and
sidecars. If a document changes while you have a draft, mdReview keeps the draft and asks you to
finish or discard it before reload.

## Sidecars and agent handoff

Sidecars are UTF-8, formatted, schema-version-1 JSON. See
[schema/review-v1.schema.json](schema/review-v1.schema.json) for the complete format.

A text anchor stores its original Markdown range and source. On reload, mdReview first checks the
original range. If that range no longer matches, it attaches to one exact unique occurrence. It
does not use fuzzy or semantic matching.

Use this workflow with an agent:

1. Ask the agent to read `open` threads in each relevant `*.md.review.json` sidecar.
2. Ask the agent to change the paired Markdown file.
3. Keep unclear or incomplete threads `open`.
4. Ask the agent to append an `agent` reply to each completed thread.
5. Ask the agent to set each completed thread to `handled`.
6. Review the result in mdReview.
7. Set accepted threads to `resolved` yourself.

The sidecar is the complete agent integration surface. mdReview has no MCP server, agent API, model
invocation, or automatic comment delivery.

## Manage the Agent Skill

Run:

```bash
mdreview skill status
mdreview skill install --target codex
mdreview skill install --target claude
mdreview skill install --target pi
mdreview skill uninstall --target codex --target claude --target pi
```

Valid targets are `codex`, `claude`, and `pi`. Install writes or replaces the selected `SKILL.md`
files. Uninstall removes a selected `SKILL.md`. It removes the `mdreview` directory only if the
directory is empty.

## Build and test from source

The build requires Go 1.26.5, Node.js 26.2.0, and npm 11.13.0.

```bash
npm --prefix web ci
./scripts/build-dev.sh
./build/mdreview --version
```

Run the checks:

```bash
GOTOOLCHAIN=local GOWORK=off GOENV=off go test ./...
npm --prefix web run format:check
npm --prefix web run lint
npm --prefix web run test:unit
npm --prefix web run build
npm --prefix web run test:browser
npm --prefix web run test:go-server
```

The compiled-server browser tests require `./build/mdreview`. Build it before you run
`test:go-server`.

## Make a release archive

Use one packaging command for both supported targets:

```bash
./scripts/package-release.sh linux/amd64
./scripts/package-release.sh darwin/arm64
```

The command makes deterministic archives, checksums, notices, and SPDX metadata. It also compares
two clean builds.

Run the final macOS check on an Apple silicon Mac:

```bash
./mdreview --version
./mdreview /path/to/project
```

Open the printed URL. Open a Markdown file and add a comment. Press `Ctrl+C` and confirm that the
process stops.

## Security

mdReview accepts local browser connections only. It validates the host and mutation origin. It
accepts workspace-relative paths and derives each writable sidecar path on the server. It rejects
traversal, symlinks, special files, invalid data, and oversized data.

These controls protect project data from remote connections and malicious browser pages. They do
not protect the project from another process that runs as the same user. Such a process can already
read and change the project.

Do not expose mdReview through a reverse proxy, port forward, or remote host. See
[SECURITY.md](SECURITY.md) for the complete security boundary and reporting process.

## Limits

| Input or resource      | Limit        |
| ---------------------- | -----------: |
| Markdown document      |        8 MiB |
| Review sidecar         |        8 MiB |
| Relative image asset   |       20 MiB |
| Mutation request body  |        2 MiB |
| Review message body    | 64 KiB UTF-8 |
| Text-anchor source     |  1 MiB UTF-8 |
| One `.gitignore` file  |        1 MiB |

mdReview loads relative PNG, JPEG, GIF, and WebP images through its validated asset route. It
rejects SVG and unsupported image types.

## Troubleshooting

- If no browser opens, copy the printed URL into a browser.
- If port `4242` is busy, use the port in the printed URL.
- If no documents appear, check the served directory and its `.gitignore` rules.
- If a sidecar is read-only, correct its reported error. mdReview does not repair invalid sidecars.
- If a thread is detached, its source is missing or occurs more than once.
- If setup is not interactive, use `skill install` with explicit targets.
- If the process must stop, press `Ctrl+C` in its terminal.

## Update

Stop mdReview. Download the new archive. Repeat the installation procedure for your operating
system. Your Markdown and sidecars remain in the project.

## Uninstall

Remove installed skill files:

```bash
mdreview skill uninstall --target codex --target claude --target pi
```

Remove the binary:

```bash
rm -- "$HOME/.local/bin/mdreview"
```

This does not delete project sidecars.

## Licence

- [Security policy](SECURITY.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)
- [MIT licence](LICENSE)
