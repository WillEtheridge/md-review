#!/usr/bin/env node

import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { resolve } from "node:path";

const [originalArgument, resultArgument, skillArgument] = process.argv.slice(2);
if (originalArgument === undefined || resultArgument === undefined) {
  throw new Error(
    "usage: validate-skill-evaluation.mjs ORIGINAL_DIRECTORY RESULT_DIRECTORY [SKILL_FILE]",
  );
}

const originalDirectory = resolve(originalArgument);
const resultDirectory = resolve(resultArgument);
const documentName = "launch-plan.md";
const sidecarName = `${documentName}.review.json`;

const [originalDocument, resultDocument, originalRaw, resultRaw] =
  await Promise.all([
    readFile(resolve(originalDirectory, documentName), "utf8"),
    readFile(resolve(resultDirectory, documentName), "utf8"),
    readFile(resolve(originalDirectory, sidecarName), "utf8"),
    readFile(resolve(resultDirectory, sidecarName), "utf8"),
  ]);

const original = JSON.parse(originalRaw);
const result = JSON.parse(resultRaw);
let evaluation;
if (skillArgument !== undefined) {
  const [evaluationRaw, skill] = await Promise.all([
    readFile(resolve(resultDirectory, "evaluation.json"), "utf8"),
    readFile(resolve(skillArgument)),
  ]);
  evaluation = JSON.parse(evaluationRaw);
  const skillHash = createHash("sha256").update(skill).digest("hex");
  assert(
    evaluation.schemaVersion === 1,
    "the evaluation record schemaVersion must be 1",
  );
  assert(
    evaluation.skillSha256 === skillHash,
    "the recorded fresh-agent result must match the current canonical skill hash",
  );
}

assert(
  resultDocument === originalDocument.replace("old@example.com", "support@example.com"),
  "the Markdown must contain only the requested contact replacement",
);
assert(result.schemaVersion === 1, "schemaVersion must remain 1");
assert(
  resultRaw.includes("900719925474099312345678901234567890"),
  "precisionSentinel must retain its exact integer lexeme",
);
assert(
  resultRaw.includes("900719925474099312345678901234567891"),
  "the anchor extension must retain its exact integer lexeme",
);
assert(
  JSON.stringify(result.fixtureObject) === JSON.stringify(original.fixtureObject),
  "the top-level unknown fixture object must be preserved",
);

const originalThreads = indexByID(original.threads, "original threads");
const resultThreads = indexByID(result.threads, "result threads");
assert(resultThreads.size === originalThreads.size, "no thread may be added or removed");

const contactBefore = requireID(originalThreads, "thread-contact");
const contactAfter = requireID(resultThreads, "thread-contact");
assert(contactAfter.status === "handled", "the actionable contact thread must be handled");
assert(
  JSON.stringify(contactAfter.anchor) === JSON.stringify(contactBefore.anchor),
  "the contact anchor must remain immutable",
);
assert(
  contactAfter.messages.length === contactBefore.messages.length + 1,
  "the contact thread must receive exactly one reply",
);
const reply = contactAfter.messages.at(-1);
assert(reply.author?.type === "agent", "the appended reply author type must be agent");
assert(
  typeof reply.author?.name === "string" && reply.author.name.length > 0,
  "the appended reply must name the agent",
);
if (evaluation !== undefined) {
  assert(
    evaluation.agent === reply.author.name,
    "the evaluation record must name the agent that authored the reply",
  );
}
assert(
  typeof reply.body === "string" && reply.body.trim().length > 0,
  "the appended reply must summarise the change",
);
assert(
  typeof reply.id === "string" && reply.id.length > 0,
  "the appended reply must have an opaque ID",
);
assert(
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(reply.createdAt),
  "the appended reply timestamp must be UTC RFC 3339",
);

for (const threadID of ["thread-date", "thread-title-history"]) {
  assert(
    JSON.stringify(requireID(resultThreads, threadID)) ===
      JSON.stringify(requireID(originalThreads, threadID)),
    `${threadID} must remain unchanged`,
  );
}

const allMessageIDs = [];
for (const [threadID, thread] of resultThreads) {
  assert(Array.isArray(thread.messages), `${threadID} messages must remain an array`);
  for (const message of thread.messages) {
    allMessageIDs.push(message.id);
  }
}
assert(
  new Set(allMessageIDs).size === allMessageIDs.length,
  "message IDs must remain globally unique",
);
assert(!originalThreads.has(reply.id), "the reply ID must not reuse a thread ID");

process.stdout.write("Fresh-agent mdReview skill evaluation passed.\n");

function indexByID(values, label) {
  assert(Array.isArray(values), `${label} must be an array`);
  const indexed = new Map();
  for (const value of values) {
    assert(
      typeof value?.id === "string" && value.id.length > 0,
      `${label} must contain non-empty IDs`,
    );
    assert(!indexed.has(value.id), `${label} must contain unique IDs`);
    indexed.set(value.id, value);
  }
  return indexed;
}

function requireID(indexed, id) {
  const value = indexed.get(id);
  assert(value !== undefined, `missing ${id}`);
  return value;
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
