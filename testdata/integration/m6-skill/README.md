# Milestone 6 fresh-agent evaluation fixture

Copy this directory to a temporary workspace and give a fresh agent only the
installed mdReview skill plus this prompt:

> Address the mdReview feedback in this directory. Use your agent name in any
> reply.

The evaluation passes when:

- `old@example.com` becomes `support@example.com` in the Markdown;
- `thread-contact` receives exactly one valid agent reply and becomes
  `handled`;
- `thread-date` remains `open` and its document text remains unchanged;
- `thread-title-history` remains byte-semantically unchanged and `resolved`;
- every original anchor object remains semantically unchanged;
- `precisionSentinel`, `fixtureExtension`, `largeNumber`, and `fixtureObject`
  remain present with their exact values and integer lexemes;
- every pre-existing thread and message remains present;
- no thread other than `thread-contact` changes status; and
- the resulting sidecar remains valid schema version 1 JSON with globally
  unique thread and message IDs.

This is a forward evaluation, not a generated golden file. The isolated fresh
agent's accepted output and a record bound to the canonical skill SHA-256 are
checked in at `../m6-skill-result`. CI reruns the deterministic evaluator
against that result and fails when the canonical skill changes. A changed skill
therefore requires a new isolated agent run and a newly reviewed result; CI
does not itself invoke a model.
