import { describe, expect, it } from "vitest";

import type { NavigationNode } from "./api";
import {
  ancestorDirectoryPaths,
  documentPaths,
  filterNavigation,
  findDocument,
  orderNavigation
} from "./navigation";

const unordered: NavigationNode[] = [
  {
    kind: "document",
    name: "zeta.md",
    path: "zeta.md",
    sizeBytes: 1,
    availability: "ready"
  },
  {
    kind: "directory",
    name: "Guides",
    path: "Guides",
    children: [
      {
        kind: "document",
        name: "Beta.md",
        path: "Guides/Beta.md",
        sizeBytes: 2,
        availability: "tooLarge"
      },
      {
        kind: "document",
        name: "alpha.md",
        path: "Guides/alpha.md",
        sizeBytes: 2,
        availability: "ready"
      }
    ]
  },
  {
    kind: "document",
    name: "Alpha.md",
    path: "Alpha.md",
    sizeBytes: 1,
    availability: "ready"
  },
  {
    kind: "directory",
    name: "api",
    path: "api",
    children: [
      {
        kind: "document",
        name: "reference.md",
        path: "api/reference.md",
        sizeBytes: 2,
        availability: "ready"
      }
    ]
  }
];

describe("navigation", () => {
  it("sorts directories first and names case-insensitively with a stable tie-breaker", () => {
    const ordered = orderNavigation(unordered);

    expect(ordered.map((node) => node.name)).toEqual(["api", "Guides", "Alpha.md", "zeta.md"]);
    expect(
      ordered[1]?.kind === "directory" ? ordered[1].children.map(({ name }) => name) : []
    ).toEqual(["alpha.md", "Beta.md"]);
  });

  it("filters document filenames only while retaining their ancestor directories", () => {
    const filtered = filterNavigation(unordered, "BETA");

    expect(filtered).toEqual([
      {
        kind: "directory",
        name: "Guides",
        path: "Guides",
        children: [
          {
            kind: "document",
            name: "Beta.md",
            path: "Guides/Beta.md",
            sizeBytes: 2,
            availability: "tooLarge"
          }
        ]
      }
    ]);
    expect(filterNavigation(unordered, "guides")).toEqual([]);
  });

  it("finds and enumerates nested indexed documents", () => {
    expect(findDocument(unordered, "Guides/Beta.md")?.availability).toBe("tooLarge");
    expect(findDocument(unordered, "missing.md")).toBeUndefined();
    expect(Array.from(documentPaths(unordered)).sort()).toEqual([
      "Alpha.md",
      "Guides/Beta.md",
      "Guides/alpha.md",
      "api/reference.md",
      "zeta.md"
    ]);
  });

  it("lists only the containing directories for a document path", () => {
    expect(ancestorDirectoryPaths("Guides/reference/index.md")).toEqual([
      "Guides",
      "Guides/reference"
    ]);
    expect(ancestorDirectoryPaths("README.md")).toEqual([]);
  });
});
