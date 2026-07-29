import { render } from "preact";

import { buildRenderModel, SpikeDocument } from "./markdown";
import { mapDomRange, restoreDomRange } from "./selection";
import type { MappingResult, RenderModel, TextAnchor } from "./types";
import "./style.css";

interface TextPointRequest {
  needle: string;
  occurrence?: number;
  offset?: number;
  edge?: "start" | "end";
}

interface TextSelectionRequest {
  start: TextPointRequest;
  end: TextPointRequest;
}

interface SpikeApi {
  getSource(): string;
  getLeaves(): Array<{ id: string; text: string; boundaries: Array<number | null> }>;
  selectText(request: TextSelectionRequest): string;
  selectElement(selector: string): string;
  mapSelection(): MappingResult;
  restore(anchor: Pick<TextAnchor, "start" | "end">): string | null;
  rerender(): Promise<void>;
  setSource(source: string): Promise<void>;
}

declare global {
  interface Window {
    mdReviewSpike?: SpikeApi;
  }
}

const mount = document.querySelector<HTMLElement>("#app");
if (!mount) {
  throw new Error("Missing #app");
}
const mountElement: HTMLElement = mount;

let model: RenderModel;
let generation = 0;

function documentRoot(): HTMLElement {
  const root = document.querySelector<HTMLElement>("#document");
  if (!root) {
    throw new Error("Document is not rendered");
  }
  return root;
}

function textNodes(root: HTMLElement): Text[] {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  let current = walker.nextNode();
  while (current) {
    nodes.push(current as Text);
    current = walker.nextNode();
  }
  return nodes;
}

function findTextPoint(root: HTMLElement, request: TextPointRequest): {
  node: Text;
  offset: number;
} {
  const occurrence = request.occurrence ?? 0;
  let seen = 0;
  for (const node of textNodes(root)) {
    let from = 0;
    while (from <= node.data.length) {
      const index = node.data.indexOf(request.needle, from);
      if (index < 0) {
        break;
      }
      if (seen === occurrence) {
        const base =
          request.edge === "end" ? index + request.needle.length : index;
        return {
          node,
          offset: base + (request.offset ?? 0)
        };
      }
      seen += 1;
      from = index + Math.max(request.needle.length, 1);
    }
  }
  throw new Error(
    `Could not find occurrence ${occurrence} of ${JSON.stringify(request.needle)}`
  );
}

function selectedRange(): Range {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount !== 1) {
    throw new Error("Expected exactly one browser range");
  }
  return selection.getRangeAt(0);
}

function installRange(range: Range): string {
  const selection = window.getSelection();
  if (!selection) {
    throw new Error("Selection API is unavailable");
  }
  selection.removeAllRanges();
  selection.addRange(range);
  return range.toString();
}

async function showSource(source: string): Promise<void> {
  model = await buildRenderModel(source);
  generation += 1;
  render(<SpikeDocument model={model} generation={generation} />, mountElement);
  document.documentElement.dataset.ready = "true";
}

const api: SpikeApi = {
  getSource: () => model.source,
  getLeaves: () =>
    Array.from(model.leaves.values()).map((leaf) => ({
      id: leaf.id,
      text: leaf.text,
      boundaries: leaf.boundaries
    })),
  selectText: (request) => {
    const root = documentRoot();
    const start = findTextPoint(root, request.start);
    const end = findTextPoint(root, request.end);
    const range = document.createRange();
    range.setStart(start.node, start.offset);
    range.setEnd(end.node, end.offset);
    return installRange(range);
  },
  selectElement: (selector) => {
    const element = documentRoot().querySelector(selector);
    if (!element) {
      throw new Error(`Could not find ${selector}`);
    }
    const range = document.createRange();
    range.selectNodeContents(element);
    return installRange(range);
  },
  mapSelection: () => mapDomRange(selectedRange(), documentRoot(), model),
  restore: (anchor) => {
    const range = restoreDomRange(anchor, documentRoot(), model);
    return range ? installRange(range) : null;
  },
  rerender: async () => {
    const source = model.source;
    render(null, mountElement);
    await showSource(source);
  },
  setSource: async (source) => {
    render(null, mountElement);
    await showSource(source);
  }
};

window.mdReviewSpike = api;

const fixture =
  new URLSearchParams(window.location.search).get("fixture") ?? "plain.md";
if (!/^[a-z0-9-]+\.md$/u.test(fixture)) {
  throw new Error("Invalid fixture name");
}

const response = await fetch(`/fixtures/${fixture}`);
if (!response.ok) {
  throw new Error(`Could not load fixture ${fixture}: ${response.status}`);
}
const bytes = new Uint8Array(await response.arrayBuffer());
const source = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
await showSource(source);
