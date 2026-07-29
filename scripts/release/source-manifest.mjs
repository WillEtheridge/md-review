import { readFileSync } from "node:fs";
import { join, posix } from "node:path";

export const sourceManifestPath = "scripts/release/source-files.txt";

function fail(message) {
  throw new Error(message);
}

function isForbiddenSourcePath(path) {
  const segments = path.split("/");
  if (
    segments.includes(".git") ||
    segments.includes(".private") ||
    segments.includes("node_modules") ||
    segments.includes("coverage") ||
    segments.includes("playwright-report") ||
    segments.includes("test-results") ||
    segments[0] === "build"
  ) {
    return true;
  }
  if (segments.includes("dist")) {
    return path !== "web/dist/placeholder.txt";
  }
  return (
    path === "spikes/selection-mapping/artifacts" ||
    path.startsWith("spikes/selection-mapping/artifacts/") ||
    path.endsWith(".coverprofile") ||
    path.endsWith(".prof")
  );
}

export function parseSourceManifest(contents) {
  if (!contents.endsWith("\n")) {
    fail("source manifest must end with a newline");
  }
  const lines = contents.slice(0, -1).split("\n");
  if (lines.length === 0 || lines.some((line) => line.length === 0)) {
    fail("source manifest must contain non-empty entries");
  }

  const entries = lines.map((line) => {
    const match = /^(0644|0755) ([^\0\r\n]+)$/u.exec(line);
    if (match === null) {
      fail(`invalid source manifest entry: ${line}`);
    }
    const path = match[2];
    if (
      path.startsWith("/") ||
      path.includes("\\") ||
      path !== posix.normalize(path) ||
      path.split("/").includes("..") ||
      path.split("/").includes(".")
    ) {
      fail(`unsafe source manifest path: ${path}`);
    }
    if (isForbiddenSourcePath(path)) {
      fail(`forbidden source manifest path: ${path}`);
    }
    return { path, mode: Number.parseInt(match[1], 8) };
  });

  const paths = entries.map((entry) => entry.path);
  const sortedPaths = [...paths].sort((left, right) =>
    Buffer.compare(Buffer.from(left), Buffer.from(right))
  );
  if (paths.join("\n") !== sortedPaths.join("\n")) {
    fail("source manifest entries must be bytewise sorted");
  }
  if (new Set(paths).size !== paths.length) {
    fail("source manifest contains duplicate paths");
  }
  if (!paths.includes(sourceManifestPath)) {
    fail(`source manifest must include itself: ${sourceManifestPath}`);
  }
  if (!paths.includes("web/dist/placeholder.txt")) {
    fail("source manifest must include web/dist/placeholder.txt");
  }
  return entries;
}

export function readSourceManifest(root) {
  const path = join(root, ...sourceManifestPath.split("/"));
  return parseSourceManifest(readFileSync(path, "utf8"));
}
