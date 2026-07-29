export type LinkPolicy =
  | {
      kind: "external";
      href: string;
    }
  | {
      kind: "fragment";
      href: string;
      fragment: string;
    }
  | {
      kind: "document";
      href: string;
      path: string;
      fragment: string | null;
    }
  | {
      kind: "inert";
    };

function splitFragment(value: string): { path: string; fragment: string | null } {
  const marker = value.indexOf("#");
  if (marker < 0) {
    return { path: value, fragment: null };
  }
  return {
    path: value.slice(0, marker),
    fragment: value.slice(marker + 1)
  };
}

function decodePath(value: string): string | undefined {
  try {
    return decodeURIComponent(value);
  } catch {
    return undefined;
  }
}

function resolveRelativePath(
  currentDocumentPath: string,
  relativePath: string
): string | undefined {
  if (
    relativePath.length === 0 ||
    relativePath.startsWith("/") ||
    relativePath.startsWith("\\") ||
    relativePath.includes("\\") ||
    relativePath.includes("\0") ||
    relativePath.includes("?")
  ) {
    return undefined;
  }

  const currentParts = currentDocumentPath.split("/");
  currentParts.pop();
  for (const part of relativePath.split("/")) {
    if (part === "" || part === ".") {
      continue;
    }
    if (part === "..") {
      if (currentParts.length === 0) {
        return undefined;
      }
      currentParts.pop();
      continue;
    }
    currentParts.push(part);
  }

  return currentParts.join("/");
}

export function classifyLink(
  rawHref: string,
  currentDocumentPath: string,
  indexedDocumentPaths: ReadonlySet<string>
): LinkPolicy {
  const hasControlCharacter = Array.from(rawHref).some((character) => {
    const codePoint = character.codePointAt(0);
    return codePoint !== undefined && (codePoint < 0x20 || codePoint === 0x7f);
  });
  if (rawHref.length === 0 || hasControlCharacter) {
    return { kind: "inert" };
  }

  if (rawHref.startsWith("#")) {
    return {
      kind: "fragment",
      href: rawHref,
      fragment: rawHref.slice(1)
    };
  }

  if (/^[A-Za-z][A-Za-z0-9+.-]*:/u.test(rawHref)) {
    let url: URL;
    try {
      url = new URL(rawHref);
    } catch {
      return { kind: "inert" };
    }
    if (url.protocol === "http:" || url.protocol === "https:" || url.protocol === "mailto:") {
      return {
        kind: "external",
        href: url.href
      };
    }
    return { kind: "inert" };
  }

  if (rawHref.startsWith("//")) {
    return { kind: "inert" };
  }

  const parts = splitFragment(rawHref);
  const decodedPath = decodePath(parts.path);
  if (!decodedPath?.endsWith(".md")) {
    return { kind: "inert" };
  }
  const resolvedPath = resolveRelativePath(currentDocumentPath, decodedPath);
  if (!resolvedPath || !indexedDocumentPaths.has(resolvedPath)) {
    return { kind: "inert" };
  }

  return {
    kind: "document",
    href: rawHref,
    path: resolvedPath,
    fragment: parts.fragment
  };
}
