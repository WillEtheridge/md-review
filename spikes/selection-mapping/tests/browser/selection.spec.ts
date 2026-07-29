import { readFileSync } from "node:fs";

import { expect, test } from "@playwright/test";

type TextPoint = {
  needle: string;
  occurrence?: number;
  offset?: number;
  edge?: "start" | "end";
};

type SelectionCase = {
  id: string;
  description: string;
  fixture: string;
  selection:
    | { kind: "text"; start: TextPoint; end: TextPoint }
    | { kind: "element"; selector: string };
  expected:
    | {
        decision: "accept";
        start: number;
        end: number;
        source: string;
        text: string;
      }
    | { decision: "reject"; reason: string };
};

const selectionCases = JSON.parse(
  readFileSync(
    new URL("../fixtures/selection-cases.json", import.meta.url),
    "utf8"
  )
) as SelectionCase[];

async function openFixture(
  page: import("@playwright/test").Page,
  fixture: string
): Promise<void> {
  await page.goto(`/?fixture=${fixture}`);
  await expect(page.locator("html")).toHaveAttribute("data-ready", "true");
}

for (const selectionCase of selectionCases) {
  test(`${selectionCase.id}: ${selectionCase.description}`, async ({ page }) => {
    await openFixture(page, selectionCase.fixture);

    const selected = await page.evaluate((selection) => {
      if (selection.kind === "text") {
        return window.mdReviewSpike?.selectText({
          start: selection.start,
          end: selection.end
        });
      }
      return window.mdReviewSpike?.selectElement(selection.selector);
    }, selectionCase.selection);

    const result = await page.evaluate(() =>
      window.mdReviewSpike?.mapSelection()
    );
    expect(result).toBeDefined();
    expect(result?.decision).toBe(selectionCase.expected.decision);

    if (selectionCase.expected.decision === "reject") {
      expect(result).toEqual({
        decision: "reject",
        reason: selectionCase.expected.reason
      });
      return;
    }

    expect(selected).toBe(selectionCase.expected.text);
    expect(result?.decision).toBe("accept");
    if (!result || result.decision !== "accept") {
      return;
    }

    expect(result.anchor).toEqual({
      start: selectionCase.expected.start,
      end: selectionCase.expected.end,
      source: selectionCase.expected.source,
      text: selectionCase.expected.text
    });

    const originalEndpoints = {
      startLeafId: result.startLeafId,
      startLeafOffset: result.startLeafOffset,
      endLeafId: result.endLeafId,
      endLeafOffset: result.endLeafOffset
    };

    const sameRender = await page.evaluate((anchor) => {
      const restoredText = window.mdReviewSpike?.restore(anchor);
      const remapped = window.mdReviewSpike?.mapSelection();
      return { restoredText, remapped };
    }, result.anchor);

    expect(sameRender.restoredText).toBe(selectionCase.expected.text);
    expect(sameRender.remapped?.decision).toBe("accept");
    if (sameRender.remapped?.decision === "accept") {
      expect({
        startLeafId: sameRender.remapped.startLeafId,
        startLeafOffset: sameRender.remapped.startLeafOffset,
        endLeafId: sameRender.remapped.endLeafId,
        endLeafOffset: sameRender.remapped.endLeafOffset
      }).toEqual(originalEndpoints);
    }

    await page.evaluate(() => window.mdReviewSpike?.rerender());
    const freshRender = await page.evaluate((anchor) => {
      const restoredText = window.mdReviewSpike?.restore(anchor);
      const remapped = window.mdReviewSpike?.mapSelection();
      return { restoredText, remapped };
    }, result.anchor);

    expect(freshRender.restoredText).toBe(selectionCase.expected.text);
    expect(freshRender.remapped?.decision).toBe("accept");
    if (freshRender.remapped?.decision === "accept") {
      expect(freshRender.remapped.anchor).toEqual(result.anchor);
      expect({
        startLeafId: freshRender.remapped.startLeafId,
        startLeafOffset: freshRender.remapped.startLeafOffset,
        endLeafId: freshRender.remapped.endLeafId,
        endLeafOffset: freshRender.remapped.endLeafOffset
      }).toEqual(originalEndpoints);
    }
  });
}

test("S19: every accepted manifest case restores after a fresh render", () => {
  const accepted = selectionCases.filter(
    (selectionCase) => selectionCase.expected.decision === "accept"
  );
  expect(accepted.length).toBeGreaterThanOrEqual(18);
});

test("S20: a supplied range restores after an exact unique movement", async ({
  page
}) => {
  await openFixture(page, "restore-original.md");
  await page.evaluate(() =>
    window.mdReviewSpike?.selectText({
      start: { needle: "Target words live here." },
      end: { needle: "Target words live here.", edge: "end" }
    })
  );
  const original = await page.evaluate(() =>
    window.mdReviewSpike?.mapSelection()
  );
  expect(original?.decision).toBe("accept");
  if (!original || original.decision !== "accept") {
    return;
  }
  expect(original.anchor).toEqual({
    start: 8,
    end: 31,
    source: "Target words live here.",
    text: "Target words live here."
  });

  const movedSource = "Preface newly added.\n\nIntro.\n\nTarget words live here.\n";
  await page.evaluate((source) => window.mdReviewSpike?.setSource(source), movedSource);
  const restored = await page.evaluate(() =>
    window.mdReviewSpike?.restore({ start: 30, end: 53 })
  );
  expect(restored).toBe("Target words live here.");
  const remapped = await page.evaluate(() =>
    window.mdReviewSpike?.mapSelection()
  );
  expect(remapped?.decision).toBe("accept");
  if (remapped?.decision === "accept") {
    expect(remapped.anchor).toEqual({
      start: 30,
      end: 53,
      source: "Target words live here.",
      text: "Target words live here."
    });
  }
});

test("a real mouse double-click produces a mappable native selection", async ({
  page
}) => {
  await openFixture(page, "plain.md");
  const point = await page.evaluate(() => {
    const root = document.querySelector("#document");
    if (!root) {
      throw new Error("Missing rendered document");
    }
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    let node = walker.nextNode();
    while (node && !node.textContent?.includes("Alpha")) {
      node = walker.nextNode();
    }
    if (!node) {
      throw new Error("Missing Alpha text");
    }
    const start = node.textContent?.indexOf("Alpha") ?? -1;
    const range = document.createRange();
    range.setStart(node, start);
    range.setEnd(node, start + "Alpha".length);
    const rect = range.getBoundingClientRect();
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
  });

  await page.mouse.dblclick(point.x, point.y);
  expect(await page.evaluate(() => window.getSelection()?.toString())).toBe(
    "Alpha"
  );
  const result = await page.evaluate(() =>
    window.mdReviewSpike?.mapSelection()
  );
  expect(result?.decision).toBe("accept");
  if (result?.decision === "accept") {
    expect(result.anchor.source).toBe("Alpha");
    expect(result.anchor.start).toBe(14);
    expect(result.anchor.end).toBe(19);
  }
});

test("keyboard extension preserves a mappable native selection", async ({
  page
}) => {
  await openFixture(page, "plain.md");
  const point = await page.evaluate(() => {
    const root = document.querySelector("#document");
    if (!root) {
      throw new Error("Missing rendered document");
    }
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    let node = walker.nextNode();
    while (node && !node.textContent?.includes("Alpha")) {
      node = walker.nextNode();
    }
    if (!node) {
      throw new Error("Missing Alpha text");
    }
    const start = node.textContent?.indexOf("Alpha") ?? -1;
    const range = document.createRange();
    range.setStart(node, start);
    range.setEnd(node, start + "Alpha".length);
    const rect = range.getBoundingClientRect();
    return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
  });

  await page.mouse.dblclick(point.x, point.y);
  await page.keyboard.down("Shift");
  for (let index = 0; index < 5; index += 1) {
    await page.keyboard.press("ArrowRight");
  }
  await page.keyboard.up("Shift");

  expect(await page.evaluate(() => window.getSelection()?.toString())).toBe(
    "Alpha beta"
  );
  const result = await page.evaluate(() =>
    window.mdReviewSpike?.mapSelection()
  );
  expect(result?.decision).toBe("accept");
  if (result?.decision === "accept") {
    expect(result.anchor).toEqual({
      start: 14,
      end: 24,
      source: "Alpha beta",
      text: "Alpha beta"
    });
  }
});

test("sanitisation removes executable raw HTML", async ({ page }) => {
  await openFixture(page, "raw-html.md");
  await expect(page.locator("#document script")).toHaveCount(0);
  await expect(page.locator("#document")).not.toContainText('alert("unsafe")');
});
