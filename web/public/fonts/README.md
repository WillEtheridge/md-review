# Bundled fonts

mdReview bundles complete, unmodified webfonts from their official upstream sources frozen by the
Milestone 4 contract. The files are checked in so development, tests, and release builds never
download runtime fonts.

| Family         | Revision                                        | Commit                                     | Official source                                                                                                        |
| -------------- | ----------------------------------------------- | ------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| PT Serif       | `main@7ff85c87f93ea6cca5f41c69f2e4edcb90240f26` | `7ff85c87f93ea6cca5f41c69f2e4edcb90240f26` | [google/fonts](https://github.com/google/fonts/tree/7ff85c87f93ea6cca5f41c69f2e4edcb90240f26/ofl/ptserif)              |
| Inter          | `v4.1`                                          | `e3a3d4c57d5ecc01453a575621882a384c1995a3` | [Inter-4.1.zip](https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip)                                    |
| JetBrains Mono | `v2.304`                                        | `cd5227bd1f61dff3bbd6c814ceaf7ffd95e947d9` | [JetBrainsMono-2.304.zip](https://github.com/JetBrains/JetBrainsMono/releases/download/v2.304/JetBrainsMono-2.304.zip) |

The PT Serif licence file is the exact `OFL.txt` from the frozen Google Fonts revision. The Inter
and JetBrains Mono licence files come directly from their release archives.

No font has been subset, renamed, or otherwise modified. Exact file and licence hashes, styles,
weights, and source provenance are recorded in `manifest.json`.
