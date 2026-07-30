# Security policy

This document defines the security boundary for mdReview.

## Supported release

Security support applies to the latest published `v0.2.0-preview.1` release for:

- Linux `amd64`;
- Apple silicon macOS (`arm64`).

Windows and Intel macOS are not supported.

## Report a vulnerability

Do not put vulnerability details in a public issue, discussion, pull request, or review comment.

Use the repository host's private security-reporting feature. Include:

- the release version and checksum;
- the operating system;
- the steps to reproduce the problem;
- the expected and actual results;
- the minimum files necessary to show the problem.

Remove private Markdown, sidecars, user names, and absolute paths unless they are necessary.

If the repository host has no private reporting feature, use a private maintainer contact that the
host or release metadata publishes. This repository does not publish an unverified security email
address.

## Threat model

mdReview is a foreground application for one user on one computer. It is not a remote service,
sandbox, or multi-user collaboration system.

The security controls defend project data against:

- a remote network client;
- a malicious website in the user's browser;
- invalid or hostile data in Markdown, sidecars, image files, browser requests, and `.gitignore`
  files.

The controls do not defend project data against:

- another process that runs as the same user;
- an administrator or root process;
- a person with physical access to an unlocked account;
- a malicious browser extension with suitable local access.

A process that runs as the same user can already read, change, and execute files in the project.
mdReview does not use filesystem operations as a security boundary against that process.

## Network boundary

mdReview:

- binds only to `127.0.0.1`;
- validates the exact `Host` header;
- requires the expected `Origin` for browser mutations;
- accepts JSON only for mutation requests;
- does not enable cross-origin resource sharing;
- sends a restrictive content security policy and other browser security headers.

These controls stop normal remote access, DNS-rebinding access, and cross-origin browser writes.

The API has no access token. A local process that can connect to the loopback port can read the API.
A local process can also make headers that look like browser headers. Such processes are inside the
trust boundary.

Do not expose mdReview through a reverse proxy, port forward, container ingress, or remote host.

## Filesystem boundary

Workspace operations start at one canonical absolute root. The filesystem gateway:

- accepts workspace-relative paths only;
- rejects traversal and absolute client paths;
- rejects symlinked workspace files and directories;
- rejects special files;
- checks paths again immediately before access;
- applies size limits to reads and writes.

The server derives the sidecar path from an indexed Markdown document. A browser client cannot
select an arbitrary write destination. mdReview does not write Markdown, images, or other project
files.

This is a portable application boundary, not a kernel capability sandbox. A same-user process can
replace a checked path component before the next path-based operation. That process is inside the
trust boundary.

mdReview rejects invalid, ambiguous, duplicate-key, duplicate-ID, unsupported-version, unknown,
unsafe, and oversized sidecars. It does not repair or overwrite them.

## Sidecar writes and conflicts

Each browser mutation includes the Markdown revision and sidecar revision that the browser read.
mdReview rejects the mutation if either revision changed. It does not merge concurrent sidecar
changes.

Within one process, mdReview serialises mutations. For a write, it:

1. writes complete valid JSON to a temporary sibling file;
2. syncs and closes that file;
3. checks the destination revision;
4. atomically renames the temporary file.

An external writer, including a coding agent or another mdReview process, does not use the same
lock. It can replace the sidecar between the final revision check and the rename. mdReview can then
overwrite that external change. This race is not solved.

Use a sequential workflow:

1. Finish browser comments.
2. Stop browser mutations.
3. Ask the agent to change the Markdown and sidecar.
4. Reload mdReview.
5. Verify the result before you resolve threads.

Atomic rename prevents partial JSON during ordinary operation. It does not make independent writers
transactional. mdReview does not promise survival across sudden power loss or operating-system
failure.

## Agent and process boundary

The sidecar is the complete agent integration surface. mdReview does not:

- invoke a model;
- send comments to an agent;
- expose an MCP server;
- expose an agent comment API.

An agent can append a reply and set completed work to `handled`. Only the human reviewer sets a
thread to `resolved`.

Each mdReview invocation is an independent foreground process. The terminal or agent that starts
the process owns it and stops it. mdReview has no daemon, process registry, parent-process
inspection, or parent-death handling.

## Privacy

The release embeds its browser files and fonts. Ordinary use sends no telemetry, analytics, crash
reports, remote fonts, or source-authored remote content requests.

Installing source dependencies can use the network. Opening an external link can use the network.

## Out of scope

mdReview is not designed for:

- remote hosting;
- multiple operating-system users in one writable project;
- accounts or access control;
- real-time collaboration;
- race-free external sidecar editing;
- browser-side Markdown editing;
- fuzzy anchor recovery;
- active SVG, MDX, Mermaid, mathematics, or renderer plug-ins;
- daemon, tray, or unattended service operation.
