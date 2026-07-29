# mdReview

mdReview is a local Linux application to review Markdown documents that coding agents make.

Use mdReview when an agent makes a plan, a specification, or another Markdown document. Read the
document in a browser. Add a comment to the document or to selected text.

mdReview saves comments in a sidecar file next to the Markdown file:

```text
plan.md
plan.md.review.json
```

You can give the sidecar file to a coding agent after you complete review. The agent can read the
comments and change the Markdown file. mdReview does not change Markdown files. It does not start an
agent. It does not send comments to an agent.

mdReview also includes an optional Agent Skill. Install the skill to tell a coding agent where to
find sidecar comments and how to use them. You choose if and when to install the skill.

![The mdReview interface with files, a Markdown document, and comments](web/tests/visual/m4-1440.visual.spec.ts-snapshots/rich-discussion-light-chromium-1440-linux.png)

The v0.1 release target is Linux `amd64` (`GOAMD64=v1`).

## Use mdReview to

- Review a Markdown document that a coding agent makes.
- Add comments to a document or to selected text.
- Save comments with the Markdown file.
- Give clear review data to a coding agent.

## Download and run

From the GitHub release page, download the Linux `amd64` archive and extract it. Then run the binary
against the directory you want to review:

```bash
tar -xzf mdreview-v0.1.0-linux-amd64.tar.gz
./mdreview-v0.1.0-linux-amd64/mdreview /path/to/your/markdown
```

It runs in the foreground and prints a local URL. Open that URL in your browser, then press `Ctrl+C`
in the terminal when you are finished.

## Install for repeated use

To run `mdreview` from any directory, copy it into your local binary directory:

```bash
mkdir -p "$HOME/.local/bin"
cp mdreview-v0.1.0-linux-amd64/mdreview "$HOME/.local/bin/"
mdreview .
```

Make sure `$HOME/.local/bin` is on `PATH`.

## Update

Download and extract a newer release in the same way. Stop any running mdReview process with
`Ctrl+C`, then replace the installed binary:

```bash
cp mdreview-v0.1.1-linux-amd64/mdreview "$HOME/.local/bin/"
mdreview --version
```

Your Markdown and adjacent review sidecars stay in your project directory, so an update does not
migrate or alter your review data.

## Build from source

Building requires exactly Go 1.26.5, Node.js 26.2.0, and npm 11.13.0. Dependency installation may
use the network; the resulting application has no Node.js runtime dependency.

From an extracted source release:

```bash
npm --prefix web ci
npm --prefix web run build
rm -- web/dist/placeholder.txt
mkdir -p build
GOTOOLCHAIN=local GOWORK=off GOENV=off \
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 \
  go build -mod=readonly -trimpath -buildvcs=false -ldflags=-buildid= \
  -o build/mdreview ./cmd/mdreview
```

The checked-in placeholder keeps a clean source tree valid for `go:embed`; the real frontend build
is produced first, and the exact scaffold file is removed before compilation so it cannot enter the
binary.

The release packaging and complete clean verification use a pinned environment. A locally built
binary is not the certified release artifact unless it matches the recorded artifact hash.

## Use it

Run mdReview in the directory containing the Markdown you want to review:

```bash
cd /path/to/project
mdreview .
```

It runs in the foreground, binds only to `127.0.0.1`, and prints the canonical directory, discovered
document count, and a local URL:

```text
mdReview

Directory: /path/to/project
Documents: 14
URL:       http://127.0.0.1:4242/

Waiting for a browser connection. Press Ctrl+C to stop.
```

mdReview does not open a browser. Open the printed URL yourself. Press `Ctrl+C` in the terminal to
stop the application. v0.1 has no daemon, tray application, or separate stop command.

Then:

1. Choose a Markdown file from the left-hand file tree.
2. Select text in the document, or use the document-level comment action.
3. Write a comment, reply in a thread, and resolve it when the review is done.
4. Commit or share the adjacent `.md.review.json` sidecar when you want the feedback to travel with
   the document.

With no directory argument, mdReview serves the current directory. It first tries port `4242`, then
reports another available port if needed:

```bash
mdreview
mdreview /path/to/project
mdreview --port 4800 /path/to/project
```

An explicit busy port is an error. Different directories can run in separate foreground processes on
different ports. Starting the same canonical directory again as the same Unix user reports the
existing instance's URL instead of starting a second writer.

Use `mdreview --version` to confirm the installed release. The v0.1 CLI has no public `--help` flag;
use the command reference below for supported commands.

## Review a document

The interface has three fixed, independently scrolling panes:

1. **Files** discovers `.md` files recursively, respects `.gitignore`, and filters by filename.
2. **Document** renders sanitised GitHub-Flavoured Markdown with editorial typography.
3. **Review** holds document comments, text comments, replies, and status controls.

Choose Light, Dark, or System theme. The layout is desktop-only and remains fixed at narrow widths,
where the page scrolls horizontally.

To leave feedback:

1. Select a contiguous representable text range and choose **Comment**, or use **Add document
   comment**.
2. Write the comment. `Ctrl+Enter` submits it and `Escape` cancels.
3. Use a thread to reply, edit a human message, resolve or reopen feedback, or delete a thread that
   has no replies.

Text selections can cross paragraphs. mdReview offers the Comment action only when both boundaries
map unambiguously to exact Markdown source bytes. Resolved threads are hidden by default and remain
available through the filter.

The browser polls once per second while its tab is active. It notices added, changed, removed, and
renamed documents and sidecars without a background filesystem watcher. With no active browser
request, the service does not scan. If the active document changes while a comment draft exists,
mdReview keeps the draft and asks you to finish or discard it before reloading.

## Sidecars and agent handoff

The first submitted thread creates a sidecar beside its document:

```text
guide.md
guide.md.review.json
```

Removing only the trailing `.review.json` pairs a sidecar with its Markdown file. Sidecars are
UTF-8, pretty-printed, schema-version-1 JSON. The published shape is documented by
[schema/review-v1.schema.json](schema/review-v1.schema.json).

Threads have two independent properties:

- status is `open`, `handled`, or `resolved`;
- a text anchor is currently attached or detached.

Text anchors retain immutable original evidence: an exact half-open UTF-8 byte range, its exact
Markdown source, and the visible selected text. On reload, mdReview attaches at the original range
if it still matches, otherwise at one exact unique occurrence. No occurrence or several occurrences
makes the thread detached. It does not guess with fuzzy or semantic matching.

When you ask a coding agent to address feedback, the intended grammar is:

1. Find the relevant `*.md.review.json` sidecars and read `open` threads.
2. Inspect and edit the paired Markdown directly.
3. Leave unclear or incomplete threads `open`.
4. For each completed change, append an `agent` message explaining what changed, then set that
   thread to `handled`.
5. Preserve anchors, unrelated threads, and unknown schema-version-1 values.
6. Never have an agent set a thread to `resolved`.
7. Review the result in mdReview and resolve it yourself when accepted.

The sidecar is the complete agent integration surface. There is no mdReview comment API, MCP server,
automatic comment delivery, or automatic model invocation.

## Optional Agent Skill

mdReview includes an optional instruction-only Agent Skill containing the workflow above. The
application works without it.

Interactive setup detects installed Codex, Claude Code, and Gemini CLI hosts, then asks about each
detected target before making changes:

```bash
mdreview setup
```

For non-interactive management, select every target explicitly:

```bash
mdreview skill status
mdreview skill install --target codex
mdreview skill install --target claude --target gemini
mdreview skill uninstall --target codex --target claude --target gemini
```

Valid targets are `codex`, `claude`, and `gemini`. Direct installation and uninstallation never
infer targets. `--force` is accepted only by `skill install`; it moves a conflicting target to a
recoverable sibling backup before installing, rather than deleting the conflict.

The canonical skill and ownership record live under:

```text
${XDG_DATA_HOME:-$HOME/.local/share}/mdreview/
```

The installed host entries are:

| Target      | Entry                                             |
| ----------- | ------------------------------------------------- |
| Codex       | `$HOME/.agents/skills/mdreview` directory symlink |
| Claude Code | `$HOME/.claude/skills/mdreview` managed copy      |
| Gemini CLI  | `$HOME/.gemini/skills/mdreview` directory symlink |

Gemini CLI may also discover the Codex `.agents/skills` entry. Declining the Gemini target means
mdReview does not write under `.gemini`; it does not make a Codex-selected skill invisible to
Gemini.

Before uninstalling or repairing an installation, use `mdreview skill status`. Uninstall removes
only an unchanged mdReview-owned target and restores its recorded backup when one exists. Modified,
broken, ambiguous, and unowned entries are preserved for manual inspection. The canonical copy is
removed only after the final managed target is safely removed and the canonical content remains
unchanged.

No Codex, Claude Code, or Gemini CLI version is currently documented as automatically stopping
mdReview at agent-session exit. The skill instructs the user to start an ordinary foreground
instance.

## Security and privacy

mdReview is a local tool, not a sandbox or remote collaboration server. It serves a normal loopback
URL with no access token.

Workspace access is descriptor-relative and rejects symlinked workspace files and directories.
Browser writes are limited to the sidecar derived from an indexed Markdown document; mdReview never
writes Markdown or a client-supplied arbitrary path. Markdown, sidecars, image files, browser
requests, and ignore files are still treated as untrusted input.

Exact loopback binding and `Host` validation prevent remote and DNS-rebinding access. Browser writes
additionally require the exact same origin and JSON. Any local process or user able to connect to
the loopback port can read the API, and local software can forge browser headers, so all users and
processes on the machine are inside v0.1's network trust boundary. Do not expose mdReview through a
reverse proxy or use it on an untrusted shared machine.

Browser sidecar mutations are semantic, bounded, and atomic. A direct external writer, including an
agent, does not share mdReview's final transaction. An external replacement can still race between
mdReview's last revision check and its rename, so simultaneous browser/agent editing is not lossless
or race-free. Finish reviewing before asking an agent to edit, then reload and verify the result. If
directory sync fails after a rename, mdReview reports that the change was applied but crash
durability is uncertain.

The release embeds its browser assets and makes no telemetry, analytics, remote-font, or
remote-content requests during ordinary use. Installing source dependencies and following a
user-opened external link may use the network. See [SECURITY.md](SECURITY.md) for the complete
boundary and reporting process.

## Limits

| Input or resource                                |                    v0.1 limit |
| ------------------------------------------------ | ----------------------------: |
| Markdown document                                |                         8 MiB |
| Review sidecar                                   |                         8 MiB |
| Relative image asset                             |                        20 MiB |
| Retained image blobs per active document and tab | 40 MiB, four concurrent loads |
| Mutation request body                            |                         2 MiB |
| Review message body                              |                  64 KiB UTF-8 |
| New text-anchor source                           |                   1 MiB UTF-8 |
| One `.gitignore` file                            |                         1 MiB |

Relative PNG, JPEG, GIF, and WebP images are loaded through the contained asset path. Active SVG and
unsupported image types are rejected. The encoded image budget does not claim to bound
browser-internal decoded image memory.

## Troubleshooting

- **No browser opened:** this is expected. Copy the complete printed URL into your browser.
- **Port 4242 was not used:** another process owns it, so mdReview selected and printed an available
  port. An explicit `--port` never falls back.
- **The command printed an existing URL:** the same Unix user already has a healthy mdReview
  instance for that canonical directory. Use that URL.
- **No documents appear:** only `.md` files are discovered. Check the served directory and
  applicable `.gitignore` rules.
- **A file is listed but will not open:** invalid UTF-8 and Markdown over 8 MiB are visible but
  cannot be rendered or reviewed.
- **The review pane is read-only:** inspect the reported invalid, duplicate, unsupported-version,
  unsafe, or over-8-MiB sidecar. mdReview does not repair or overwrite it.
- **A thread is detached:** its original exact source is now missing or occurs more than once. This
  is honest review history, not data loss.
- **A document changed while composing:** finish or discard the draft to load the current file.
- **`mdreview setup` refuses to run:** interactive setup needs terminal input. Use direct
  `skill install` with explicit targets for scripted operation.
- **Skill status reports a conflict or modification:** inspect and back up the entry. `--force`
  preserves a conflicting target as a backup but never overwrites a modified canonical skill.
- **The process should stop:** return to its terminal and press `Ctrl+C`. v0.1 intentionally has no
  daemon or remote stop command.

## Uninstall

First remove any unchanged managed Agent Skill entries:

```bash
mdreview skill uninstall --target codex --target claude --target gemini
mdreview skill status
```

Then remove the binary from the path where you installed it:

```bash
rm -- "$HOME/.local/bin/mdreview"
```

The application does not create a database. Review sidecars are project data and are not deleted by
uninstalling mdReview.

## Licence and security

- [Security policy](SECURITY.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)
- [MIT licence](LICENSE)
