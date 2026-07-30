import { expect, test, type Locator, type Page, type Route } from "@playwright/test";

const revision = "7".repeat(64);
const longTableCell = `wide-${"table-column-".repeat(48)}`;
const longCodeLine = `const immutableBoundary = "${"code-column-".repeat(56)}";`;
const repeatedParagraphs = Array.from(
  { length: 28 },
  (_, index) =>
    `Paragraph ${String(index + 1)} keeps the document panel independently scrollable while preserving a stable test fixture.`
).join("\n\n");

const markdown = `# Fixed layout evidence

The three review regions retain their exact desktop columns at every supported
v0.1 viewport.

| Boundary | Deliberately wide value |
| --- | --- |
| Layout | ${longTableCell} |

\`\`\`ts
${longCodeLine}
\`\`\`

${repeatedParagraphs}
`;

interface ViewportCase {
  name: string;
  width: number;
  height: number;
  pageWidth: number;
  documentWidth: number;
}

interface LayoutGeometry {
  viewportWidth: number;
  pageWidth: number;
  shellWidth: number;
  filesWidth: number;
  documentWidth: number;
  reviewWidth: number;
}

const viewportCases: readonly ViewportCase[] = [
  {
    name: "1440x1000",
    width: 1440,
    height: 1000,
    pageWidth: 1440,
    documentWidth: 840
  },
  {
    name: "1280x800",
    width: 1280,
    height: 800,
    pageWidth: 1280,
    documentWidth: 680
  },
  {
    name: "900x700",
    width: 900,
    height: 700,
    pageWidth: 1000,
    documentWidth: 400
  }
];

function navigationDocuments(): unknown[] {
  return Array.from({ length: 32 }, (_, index) => {
    const sequence = String(index + 1).padStart(2, "0");
    return {
      kind: "document",
      name: `reference-${sequence}.md`,
      path: `docs/reference-${sequence}.md`,
      sizeBytes: 2048 + index,
      availability: "ready",
      documentMetadataRevision: revision,
      reviewMetadataRevision: null
    };
  });
}

function reviewThreads(): unknown[] {
  return Array.from({ length: 14 }, (_, index) => {
    const sequence = String(index + 1).padStart(2, "0");
    return {
      id: `thread_layout_${sequence}`,
      anchor: {
        type: "document"
      },
      attachment: {
        state: "document"
      },
      status: index % 3 === 0 ? "handled" : "open",
      messages: [
        {
          id: `message_layout_${sequence}`,
          author: {
            type: "human",
            name: "Reviewer"
          },
          body: `Stable review message ${sequence} keeps the review panel independently scrollable.`,
          createdAt: "2026-07-28T14:30:00Z"
        }
      ]
    };
  });
}

async function fulfillJSON(route: Route, status: number, body: unknown): Promise<void> {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body)
  });
}

async function mockLayoutWorkspace(page: Page): Promise<void> {
  const threads = reviewThreads();
  await page.route("**/api/**", async (route) => {
    const requestURL = new URL(route.request().url());
    if (requestURL.pathname === "/api/state") {
      if (requestURL.searchParams.has("since")) {
        await fulfillJSON(route, 200, {
          status: "unchanged",
          workspaceRevision: 1
        });
        return;
      }
      await fulfillJSON(route, 200, {
        status: "changed",
        workspaceRevision: 1,
        documentCount: 33,
        initialDocumentPath: "README.md",
        navigation: [
          {
            kind: "directory",
            name: "docs",
            path: "docs",
            children: navigationDocuments()
          },
          {
            kind: "document",
            name: "README.md",
            path: "README.md",
            sizeBytes: new TextEncoder().encode(markdown).length,
            availability: "ready",
            documentMetadataRevision: revision,
            reviewMetadataRevision: revision
          }
        ],
        warnings: []
      });
      return;
    }
    if (requestURL.pathname === "/api/document") {
      await fulfillJSON(route, 200, {
        path: "README.md",
        revision,
        source: markdown
      });
      return;
    }
    if (requestURL.pathname === "/api/review") {
      await fulfillJSON(route, 200, {
        path: "README.md",
        documentRevision: revision,
        reviewRevision: "8".repeat(64),
        threads
      });
      return;
    }
    await fulfillJSON(route, 404, {
      error: {
        code: "endpointNotFound",
        message: "This API endpoint does not exist."
      }
    });
  });
}

async function layoutGeometry(page: Page): Promise<LayoutGeometry> {
  return page.evaluate(() => {
    const shell = document.querySelector<HTMLElement>(".app-shell");
    const files = document.querySelector<HTMLElement>(".files-panel");
    const documentPanel = document.querySelector<HTMLElement>(".document-panel");
    const review = document.querySelector<HTMLElement>(".review-panel");
    if (!shell || !files || !documentPanel || !review) {
      throw new Error("fixed layout panels are missing");
    }
    return {
      viewportWidth: window.innerWidth,
      pageWidth: document.documentElement.scrollWidth,
      shellWidth: shell.getBoundingClientRect().width,
      filesWidth: files.getBoundingClientRect().width,
      documentWidth: documentPanel.getBoundingClientRect().width,
      reviewWidth: review.getBoundingClientRect().width
    };
  });
}

async function resetPanelScroll(panels: readonly Locator[]): Promise<void> {
  for (const panel of panels) {
    await panel.evaluate((element) => {
      element.scrollTop = 0;
    });
  }
}

async function expectKeyboardPanelScroll(
  page: Page,
  panels: readonly Locator[],
  panel: Locator,
  focusTarget: Locator
): Promise<void> {
  await focusTarget.focus();
  await expect(focusTarget).toBeFocused();
  // Firefox keeps the active descendant visible when a scroll position is
  // reset. Move focus first so a previously tested panel cannot restore its
  // old offset during this panel's keyboard assertion.
  await resetPanelScroll(panels);
  await page.keyboard.press("PageDown");
  await expect.poll(() => panel.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
}

for (const viewport of viewportCases) {
  test(`preserves fixed, keyboard-operable regions at ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize({
      width: viewport.width,
      height: viewport.height
    });
    await mockLayoutWorkspace(page);
    await page.goto(`/`);
    await expect(
      page.getByRole("heading", { level: 1, name: "Fixed layout evidence" })
    ).toBeVisible();

    expect(await layoutGeometry(page)).toEqual({
      viewportWidth: viewport.width,
      pageWidth: viewport.pageWidth,
      shellWidth: viewport.pageWidth,
      filesWidth: 240,
      documentWidth: viewport.documentWidth,
      reviewWidth: 360
    });

    const filesPanel = page.getByRole("complementary", { name: "Files" });
    const documentPanel = page.getByRole("main", { name: "Document" });
    const reviewPanel = page.getByRole("complementary", { name: "Comments" });
    const panels = [filesPanel, documentPanel, reviewPanel] as const;
    for (const panel of panels) {
      await expect(panel).toHaveCSS("overflow-y", "auto");
      await expect(panel).toHaveCSS("overflow-x", "hidden");
      expect(await panel.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(
        true
      );
    }

    await expectKeyboardPanelScroll(
      page,
      panels,
      filesPanel,
      filesPanel.getByRole("button", { name: /reference-01\.md/u })
    );
    await expectKeyboardPanelScroll(page, panels, documentPanel, documentPanel);
    await expectKeyboardPanelScroll(
      page,
      panels,
      reviewPanel,
      reviewPanel.getByRole("button", { name: "Comment on document" })
    );

    const tableScroller = documentPanel.getByRole("group", { name: "Scrollable table" });
    const codeScroller = documentPanel.getByRole("group", { name: "Scrollable code block" });
    for (const scroller of [tableScroller, codeScroller]) {
      await expect(scroller).toHaveAttribute("tabindex", "0");
      expect(await scroller.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(
        true
      );
      await scroller.evaluate((element) => {
        element.scrollLeft = 0;
      });
      await scroller.focus();
      await expect(scroller).toBeFocused();
      await page.keyboard.press("ArrowRight");
      await expect
        .poll(() => scroller.evaluate((element) => element.scrollLeft))
        .toBeGreaterThan(0);
    }
  });
}
