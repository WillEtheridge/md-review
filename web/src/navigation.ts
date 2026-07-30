import type { DocumentNode, NavigationNode } from "./api";

// Navigation is copied and transformed in the browser so filtering and user
// expansion never mutate the server-owned snapshot held by reconciliation.
function compareText(left: string, right: string): number {
  const foldedLeft = left.toLowerCase();
  const foldedRight = right.toLowerCase();
  if (foldedLeft < foldedRight) {
    return -1;
  }
  if (foldedLeft > foldedRight) {
    return 1;
  }
  if (left < right) {
    return -1;
  }
  if (left > right) {
    return 1;
  }
  return 0;
}

function compareNodes(left: NavigationNode, right: NavigationNode): number {
  if (left.kind !== right.kind) {
    return left.kind === "directory" ? -1 : 1;
  }
  return compareText(left.name, right.name);
}

/** Returns a recursively sorted copy with directories before documents. */
export function orderNavigation(nodes: readonly NavigationNode[]): NavigationNode[] {
  return nodes
    .map((node): NavigationNode =>
      node.kind === "directory"
        ? {
            ...node,
            children: orderNavigation(node.children)
          }
        : node
    )
    .sort(compareNodes);
}

/** Returns a recursively filtered copy while preserving matching ancestors. */
export function filterNavigation(
  nodes: readonly NavigationNode[],
  filter: string
): NavigationNode[] {
  // Keep a directory when any descendant matches; this preserves the path a
  // user needs to reach a filtered document without expanding unrelated files.
  const foldedFilter = filter.trim().toLowerCase();
  if (foldedFilter.length === 0) {
    return [...nodes];
  }

  const result: NavigationNode[] = [];
  for (const node of nodes) {
    if (node.kind === "document") {
      if (node.name.toLowerCase().includes(foldedFilter)) {
        result.push(node);
      }
      continue;
    }

    const children = filterNavigation(node.children, foldedFilter);
    if (children.length > 0) {
      result.push({
        ...node,
        children
      });
    }
  }
  return result;
}

/** Finds an indexed document by its slash-relative identity. */
export function findDocument(
  nodes: readonly NavigationNode[],
  path: string
): DocumentNode | undefined {
  for (const node of nodes) {
    if (node.kind === "document") {
      if (node.path === path) {
        return node;
      }
      continue;
    }
    const nested = findDocument(node.children, path);
    if (nested) {
      return nested;
    }
  }
  return undefined;
}

/** Collects indexed document identities for renderer link classification. */
export function documentPaths(nodes: readonly NavigationNode[]): ReadonlySet<string> {
  const paths = new Set<string>();
  const visit = (entries: readonly NavigationNode[]): void => {
    for (const entry of entries) {
      if (entry.kind === "document") {
        paths.add(entry.path);
      } else {
        visit(entry.children);
      }
    }
  };
  visit(nodes);
  return paths;
}

/** Lists parent directory identities from workspace root toward a document. */
export function ancestorDirectoryPaths(documentPath: string): string[] {
  // The result is ordered from the workspace root toward the document so a
  // caller can expand ancestors in one deterministic pass.
  const segments = documentPath.split("/");
  segments.pop();
  return segments.map((_, index) => segments.slice(0, index + 1).join("/"));
}
