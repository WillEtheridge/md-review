# Security policy

mdReview reads and writes repository data from a local browser interface. This
document describes the v0.1 security boundary, known limitations, and private
reporting process.

## Supported release

Security support applies to the latest published mdReview v0.1 Linux `amd64`
release. A candidate is not a supported release until its published release
record includes a passing Gate E decision tied to its source and artifact
hashes.

## Report a vulnerability privately

Do not open a public issue, discussion, pull request, or review comment with
vulnerability details.

Use this repository host's private security-reporting or private security
advisory feature when it is available. Include the affected release checksum,
Linux environment, reproduction steps, expected and observed behaviour, and
the minimum files needed to demonstrate the issue. Remove private Markdown,
sidecars, user names, and absolute paths unless they are
essential to the report.

If the repository host does not offer a private reporting feature, do not
publish the details. Use a private maintainer contact exposed by that host or
the published release metadata. This repository does not invent or embed an
unverified security email address.

## v0.1 trust boundary

mdReview is a foreground, loopback-only Linux application intended for
single-user machines. It is not a sandbox, remote service, or multi-user
collaboration system.

- The server binds to `127.0.0.1`.
- Browser mutations additionally require the exact allowed origin and a JSON
  content type.
- Exact Host validation, no CORS opt-in, CSP, referrer policy, content-type
  restrictions, structural Markdown sanitisation, and safe link policies
  reduce browser-origin attacks.
- Runtime browser assets and fonts are embedded. Ordinary use sends no
  telemetry, analytics, crash reports, remote fonts, or source-authored remote
  content requests.

Loopback binding and exact `Host` validation prevent remote and DNS-rebinding
access. Exact mutation `Origin` checks and JSON-only bodies prevent ordinary
cross-origin browser writes. The API has no access token: any local process or
user able to connect to the port can read it, and local software can forge
browser headers. Treat all users and processes on the machine as inside v0.1's
network trust boundary. Do not publish mdReview through a reverse proxy, port
forward, container ingress, or remote host.

## Filesystem and write boundary

Workspace operations begin at an open canonical-root descriptor. Linux
`openat2` containment or the forced descriptor-walk fallback prevents higher
layers from substituting a path check followed by an ordinary path open.
Symlinked workspace files and directories are rejected, and special files are
not treated as Markdown, sidecars, images, or ignore files.

The browser API writes only the adjacent sidecar derived from a Markdown
document in the current index. A client cannot provide an arbitrary
filesystem destination, and mdReview does not write Markdown, images, or other
repository files.

Raw Markdown, sidecars, browser requests, images, and `.gitignore` files are
untrusted. Reads, requests, fields, and retained encoded image blobs are
bounded. Invalid, ambiguous, duplicate-key, duplicate-ID, unsupported-version,
unsafe, or oversized sidecars remain read-only and are not repaired.

## Sidecar concurrency limitation

Browser mutations are semantic, bounded, and serialised within the process.
They reread current bytes, preserve unrelated observed schema-version-1 data,
sync a same-directory temporary file, check the destination revision, and
atomically rename complete valid JSON.

Direct external writers, including coding agents, do not participate in the
application's lock. An external replacement in the final
revision-check-to-rename interval may still be overwritten. This residual race
is not solved, and mdReview does not provide lossless uncoordinated
multi-writer collaboration.

Use a sequential workflow: finish browser comments, stop making browser
mutations, ask the agent to edit the Markdown and sidecars, then reload and
verify before resolving threads. Atomic rename prevents partial JSON; it does
not make simultaneous external edits transactional.

If the sidecar file sync fails, the destination remains untouched. If
directory sync fails after rename, mdReview reports that the mutation was
applied but its crash durability is uncertain. Repeating that semantic action
blindly could duplicate it.

## Agent and lifecycle boundaries

The sidecar is the agent integration surface. mdReview does not deliver
comments automatically, invoke a model, expose an MCP server, or provide an
agent-facing comment API.

An agent may append a reply and set a successfully addressed `open` thread to
`handled`. Only the human reviewer marks a thread `resolved`.

Ordinary instances remain foreground terminal processes and stop with
`Ctrl+C`. The optional skill does not claim automatic agent-session exit for
Codex, Claude Code, or Gemini CLI without a passing integration test for an
exact host version. The current documented host-support matrix makes no such
claim.

## Out of scope

v0.1 is not designed or certified for:

- remote hosting, reverse proxies, or internet exposure;
- several Unix users sharing one writable worktree;
- accounts, permissions, or real-time collaboration;
- macOS, Windows, mobile, or responsive layouts;
- daemon, tray, or unattended service operation;
- active SVG, MDX, Mermaid, mathematics, or renderer plugins;
- browser-side Markdown editing;
- fuzzy anchor recovery; or
- race-free direct external sidecar editing.

Source dependency installation and a user choosing an external link may use
the network. The released application itself has no Node.js runtime
dependency. Browser-internal decoded-image memory is not bounded by the
application's encoded-blob budget.

The authoritative design and accepted residual risks are maintained in the
local development record. Exact release evidence and any candidate-specific
limitations belong in the separately published release record.
