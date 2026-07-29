import { readdir, rm } from "node:fs/promises";
import { URL } from "node:url";

const distributionDirectory = new URL("../dist/", import.meta.url);
const retainedEntry = "placeholder.txt";

let entries = [];
try {
  entries = await readdir(distributionDirectory, { withFileTypes: true });
} catch (error) {
  if (!(error instanceof Error) || !("code" in error) || error.code !== "ENOENT") {
    throw error;
  }
}

await Promise.all(
  entries
    .filter((entry) => entry.name !== retainedEntry)
    .map((entry) =>
      rm(new URL(entry.name, distributionDirectory), {
        force: true,
        recursive: entry.isDirectory()
      })
    )
);
