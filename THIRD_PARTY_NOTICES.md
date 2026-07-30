# Third-Party Asset and Licence Policy

**Status:** Current direct-runtime inventory; release notices are generated from the final artifact
**Scope:** bundled third-party assets and release requirements

mdReview embeds its production browser bundle, fonts, and Agent Skill into one
Go binary. Every redistributable dependency or asset included in that binary
must have its licence reviewed and its required notice shipped with the
release.

Development-only tools are recorded in lock files and the release software
bill of materials, but their code and licence text are not represented as
embedded runtime assets unless the build output actually contains them.

## Current runtime inventory

| Component | Purpose | Licence action |
| --- | --- | --- |
| Preact | Browser UI runtime | Retain its MIT licence notice with the release |
| unified and remark/rehype packages | Markdown parsing, GFM, raw-HTML processing, and sanitisation | Retain their MIT licence notices with the release |
| decode-named-character-reference | Markdown character-reference decoding | Retain its MIT licence notice with the release |
| go-git v5 `gitignore` package | Git-compatible ignore matching | Retain its Apache-2.0 licence and required notices with the release |
| mdReview source | Application code | Retain the project's MIT licence with the release |

Transitive modules and browser packages remain subject to the final
artifact-derived inventory; recording the direct dependencies here does not
replace that release check.

## Bundled fonts

mdReview bundles these complete, unmodified upstream webfonts:

- [PT Serif](https://github.com/google/fonts/tree/main/ofl/ptserif)
  `main@7ff85c87f93ea6cca5f41c69f2e4edcb90240f26`;
- [Inter](https://github.com/rsms/inter) `v4.1`; and
- [JetBrains Mono](https://github.com/JetBrains/JetBrainsMono) `v2.304`.

Each family is licensed under the SIL Open Font License 1.1. Its exact upstream
licence is checked in beside the font files. `web/public/fonts/manifest.json`
records release and commit provenance, download URLs, unmodified status, and
SHA-256 for every redistributed font and licence. The build verifies the
source and copied production assets against that manifest.

No font is subset, renamed, or modified. Future derivation requires a separate
licence and Reserved Font Name review rather than reusing this decision.

## Release requirements

Before a release:

1. inventory Go modules, bundled JavaScript packages, fonts, and other embedded
   assets from the actual source and build output;
2. distinguish development tools from redistributed code;
3. retain required copyright and licence texts in a release notice;
4. verify that generated, subset, or modified assets satisfy their licence
   conditions;
5. generate the release software bill of materials; and
6. fail release verification when a bundled asset has no recorded licence.

This document is a build and review requirement, not legal advice.
