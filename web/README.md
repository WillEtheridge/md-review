# mdReview browser application

This directory contains the TypeScript and Preact browser application. Milestone 0 provides only the
reproducible development scaffold; later milestones add the product workflows described by the
repository specification and architecture.

## TypeScript tooling compatibility

TypeScript 7.0 ships the native `tsc` executable but does not expose the JavaScript compiler API
required by `typescript-eslint`. The package manifest therefore follows the [official TypeScript
side-by-side compatibility pattern][typescript-side-by-side]:

- `@typescript/native` aliases and pins `typescript@7.0.2`, which supplies the `tsc` binary used by
  `npm run typecheck` and `npm run build`.
- `typescript` aliases and pins `@typescript/typescript6@6.0.2`, which supplies the compiler API
  consumed by `typescript-eslint`.

Keep both aliases until the pinned linter supports the TypeScript 7 API. A plain `npm ci` installs
this arrangement without peer-dependency overrides.

[typescript-side-by-side]:
  https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/#running-side-by-side-with-typescript-6-0
