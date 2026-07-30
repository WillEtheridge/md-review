---
name: mdreview
description: Address mdReview feedback stored in adjacent *.md.review.json sidecars, update the paired Markdown safely, or start the local mdReview viewer. Use when asked to inspect or handle mdReview comments, edit Markdown from review threads, or serve Markdown with mdReview.
---

# Work with mdReview

## Address review feedback

1. Find relevant `*.md.review.json` files. Pair each sidecar with the adjacent Markdown path by
   removing only the trailing `.review.json`; for example, `guide.md.review.json` reviews
   `guide.md`.
2. Read the sidecar without losing JSON number precision. Continue only when `schemaVersion` is
   exactly `1` and the schema is valid. Stop without changing an ambiguous sidecar containing
   duplicate thread IDs, duplicate message IDs anywhere in the sidecar, or duplicate JSON object
   keys at any depth.
3. Process only threads whose `status` is `open`. Inspect the paired Markdown and the thread
   messages before deciding what change is requested.
4. Treat every anchor as immutable review history. A text anchor's `range` is an exact half-open
   UTF-8 byte range in the original source, and `source` records those original bytes. Use it as
   context even when it no longer attaches uniquely, but never change any anchor, range, source, or
   display text.
5. Edit the Markdown before changing review state. If the request is unclear, incomplete, unsafe,
   or cannot be completed, leave the thread `open`.
6. For each successfully addressed thread, append one message containing:
   - a new opaque `id` unique across every message in the sidecar;
   - `author.type` set to `agent` and `author.name` set to your agent name;
   - a concise `body` summarising the completed document change; and
   - `createdAt` set to the current UTC time in RFC 3339 form.
7. Set that thread's `status` to `handled` only after both the Markdown change and agent reply
   succeed. Never set a thread to `resolved`.

Preserve unrelated threads and every unknown schema-version-1 field, including nested values and
the exact lexemes of arbitrary-precision numbers. Use lossless JSON editing; do not round-trip
unknown numbers through floating point.

Immediately before replacing a sidecar, reread both the Markdown and sidecar. Stop and re-evaluate
if either whole-file revision changed; do not merge against the latest sidecar or compare only a
target. Write the complete result to a same-directory temporary file and atomically replace the
sidecar. Treat the final
reread-to-rename interval as a residual race with uncoordinated direct writers; do not claim
lossless multi-writer safety.

Edit the Markdown and sidecar files directly. Do not invent or call an mdReview comment API, MCP
server, or comment-management CLI.

## Start the viewer

Run `mdreview [DIRECTORY]` as an ordinary foreground child process, substituting the workspace
directory or omitting it for the current directory, or ask the user to run it in their terminal.
Ask the user to open the local URL it prints. Explain that mdReview does not open a browser
automatically. The launcher owns the process and must stop it when the user ends the agent or review
session; a user running it directly stops it with Ctrl+C. Do not use `nohup` or a detached launch.
