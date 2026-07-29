import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page, type Route } from "@playwright/test";

const revision = "4".repeat(64);
const remoteImage = "https://assets.invalid/private-diagram.png";
const localImage = "./private-local.png?blocked=1";
const longCell = `wide-${"table-column-".repeat(32)}`;
const longCode = `const payload = "${"code-column-".repeat(40)}";`;

const richMarkdown = `# Rich GFM document

Paragraph with *emphasis*, **strong text**, and ~~removed text~~.

## Lists

3. Ordered from three
4. Second item
   - Nested unordered item

- [x] Completed task
- [ ] Pending task

### Quotation

> An outer quotation.
>
> > A nested quotation.

#### Table

| Name | Wide value |
| :--- | ---: |
| Alpha | ${longCell} |

##### Code

Inline \`const answer = 42\` code.

\`\`\`js
${longCode}
\`\`\`

###### Links and media

[Next document](NEXT.md#next-document)

[External documentation](https://example.com/docs)

<a href="https://example.com/raw" tabindex="7" accesskey="x" aria-label="Misleading action" aria-describedby="missing-description">Visible raw link</a>

![Architecture diagram](${remoteImage})

![](${localImage})

---
`;

const nextMarkdown = `# Next document

[Return to the rich document](README.md)
`;

interface DocumentFixture {
  path: string;
  source: string;
}

interface M4Thread {
  id: string;
  anchor: unknown;
  attachment: unknown;
  status: "open" | "handled";
  messages: unknown[];
}

interface RecordedScroll {
  behavior: ScrollBehavior | undefined;
  block: ScrollLogicalPosition | undefined;
}

function byteRange(source: string, selectedText: string): { start: number; end: number } {
  const startUTF16 = source.indexOf(selectedText);
  if (startUTF16 < 0) {
    throw new Error(`fixture is missing selected text: ${selectedText}`);
  }
  const encoder = new TextEncoder();
  return {
    start: encoder.encode(source.slice(0, startUTF16)).length,
    end: encoder.encode(source.slice(0, startUTF16 + selectedText.length)).length
  };
}

function reviewThreads(): M4Thread[] {
  const selectedText = "strong text";
  const range = byteRange(richMarkdown, selectedText);
  return [
    {
      id: "thread_m4_attached",
      anchor: {
        type: "text",
        range,
        source: selectedText,
        text: selectedText
      },
      attachment: {
        state: "attached",
        currentRange: range
      },
      status: "open",
      messages: [
        {
          id: "message_m4_attached",
          author: {
            type: "human",
            name: "Reviewer"
          },
          body: "Make this principle concrete.",
          createdAt: "2026-07-28T14:30:00Z"
        }
      ]
    },
    {
      id: "thread_m4_detached",
      anchor: {
        type: "text",
        range: {
          start: 0,
          end: 16
        },
        source: "Removed wording.",
        text: "Removed wording."
      },
      attachment: {
        state: "detached"
      },
      status: "handled",
      messages: [
        {
          id: "message_m4_detached",
          author: {
            type: "agent",
            name: "Codex"
          },
          body: "The original wording was replaced.",
          createdAt: "2026-07-28T15:00:00Z"
        }
      ]
    }
  ];
}

async function fulfillJSON(route: Route, body: unknown): Promise<void> {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body)
  });
}

async function mockWorkspace(page: Page, threads: readonly M4Thread[] = []): Promise<string[]> {
  const mediaRequests: string[] = [];
  page.on("request", (request) => {
    const url = request.url();
    if (url.includes("assets.invalid") || url.includes("private-local.png")) {
      mediaRequests.push(url);
    }
  });

  const documents = new Map<string, DocumentFixture>([
    ["README.md", { path: "README.md", source: richMarkdown }],
    ["NEXT.md", { path: "NEXT.md", source: nextMarkdown }]
  ]);

  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/state") {
      if (url.searchParams.has("since")) {
        await fulfillJSON(route, {
          status: "unchanged",
          workspaceRevision: 1
        });
        return;
      }
      await fulfillJSON(route, {
        status: "changed",
        workspaceRevision: 1,
        documentCount: 2,
        initialDocumentPath: "README.md",
        navigation: [
          {
            kind: "document",
            name: "NEXT.md",
            path: "NEXT.md",
            sizeBytes: nextMarkdown.length,
            availability: "ready",
            documentMetadataRevision: revision,
            reviewMetadataRevision: threads.length > 0 ? revision : null
          },
          {
            kind: "document",
            name: "README.md",
            path: "README.md",
            sizeBytes: richMarkdown.length,
            availability: "ready",
            documentMetadataRevision: revision,
            reviewMetadataRevision: threads.length > 0 ? revision : null
          }
        ],
        warnings: []
      });
      return;
    }

    const path = url.searchParams.get("path");
    const document = path ? documents.get(path) : undefined;
    if (url.pathname === "/api/document" && document) {
      await fulfillJSON(route, {
        path: document.path,
        revision,
        source: document.source
      });
      return;
    }
    if (url.pathname === "/api/review" && document) {
      await fulfillJSON(route, {
        path: document.path,
        documentRevision: revision,
        reviewRevision: threads.length > 0 ? revision : null,
        threads
      });
      return;
    }

    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({
        error: {
          code: "documentNotFound",
          message: "The document was not found.",
          requestId: "m4-missing"
        }
      })
    });
  });

  return mediaRequests;
}

async function openRichDocument(page: Page, threads: readonly M4Thread[] = []): Promise<string[]> {
  const mediaRequests = await mockWorkspace(page, threads);
  await page.goto(`/`);
  await expect(page.getByRole("heading", { level: 1, name: "Rich GFM document" })).toBeVisible();
  return mediaRequests;
}

async function expectNoSeriousAccessibilityViolations(page: Page): Promise<void> {
  const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
  const materialViolations = result.violations
    .filter(({ impact }) => impact === "serious" || impact === "critical")
    .map(({ id, impact, nodes }) => ({
      id,
      impact,
      targets: nodes.map(({ target }) => target)
    }));

  expect(materialViolations).toEqual([]);
}

test("presents rich GFM in safe, labelled, keyboard-focusable regions", async ({ page }) => {
  const mediaRequests = await openRichDocument(page);
  const markdown = page.getByRole("article", { name: "Document: README.md" });
  await page.getByRole("button", { name: "Light" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

  for (const [level, name] of [
    [1, "Rich GFM document"],
    [2, "Lists"],
    [3, "Quotation"],
    [4, "Table"],
    [5, "Code"],
    [6, "Links and media"]
  ] as const) {
    await expect(markdown.getByRole("heading", { level, name })).toBeVisible();
  }

  await expect(markdown.locator("em")).toHaveText("emphasis");
  await expect(markdown.locator("strong")).toHaveText("strong text");
  await expect(markdown.locator("del")).toHaveText("removed text");
  await expect(markdown.locator("ol")).toHaveAttribute("start", "3");
  await expect(markdown.locator("blockquote blockquote")).toContainText("A nested quotation.");

  const tasks = markdown.locator('input[type="checkbox"]');
  await expect(tasks).toHaveCount(2);
  await expect(tasks.nth(0)).toBeChecked();
  await expect(tasks.nth(1)).not.toBeChecked();
  await expect(tasks.nth(0)).toBeDisabled();
  await expect(tasks.nth(1)).toBeDisabled();
  await expect(markdown.getByRole("checkbox", { name: "Completed task" })).toBeChecked();
  await expect(markdown.getByRole("checkbox", { name: "Pending task" })).not.toBeChecked();

  const tableScroller = markdown.getByRole("group", { name: "Scrollable table" });
  const codeScroller = markdown.getByRole("group", { name: "Scrollable code block" });
  await expect(tableScroller.locator("table")).toBeVisible();
  await expect(codeScroller.locator("pre code")).toContainText("const payload");
  await expect(tableScroller).toHaveAttribute("tabindex", "0");
  await expect(codeScroller).toHaveAttribute("tabindex", "0");
  await tableScroller.focus();
  await expect(tableScroller).toBeFocused();
  expect(await tableScroller.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(
    true
  );
  await page.keyboard.press("ArrowRight");
  await expect
    .poll(() => tableScroller.evaluate((element) => element.scrollLeft))
    .toBeGreaterThan(0);
  await codeScroller.focus();
  await expect(codeScroller).toBeFocused();
  expect(await codeScroller.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(
    true
  );
  await page.keyboard.press("ArrowRight");
  await expect
    .poll(() => codeScroller.evaluate((element) => element.scrollLeft))
    .toBeGreaterThan(0);

  const rawLink = markdown.getByRole("link", { name: "Visible raw link" });
  await expect(rawLink).toHaveAttribute("href", "https://example.com/raw");
  await expect(rawLink).not.toHaveAttribute("tabindex");
  await expect(rawLink).not.toHaveAttribute("accesskey");
  await expect(rawLink).not.toHaveAttribute("aria-label");
  await expect(rawLink).not.toHaveAttribute("aria-describedby");
  await expect(markdown.getByRole("link", { name: "Misleading action" })).toHaveCount(0);

  await expect(markdown.getByRole("img", { name: "Image: Architecture diagram" })).toBeVisible();
  await expect(markdown.getByRole("img", { name: "Image omitted" })).toBeVisible();
  await expect(markdown.locator("img, picture, source")).toHaveCount(0);
  expect(mediaRequests).toEqual([]);
  await expectNoSeriousAccessibilityViolations(page);
});

test("keeps landmark, theme, skip-link, and SPA focus behavior coherent", async ({ page }) => {
  await openRichDocument(page);

  await expect(page.getByRole("complementary", { name: "Files" })).toBeVisible();
  const documentLandmark = page.getByRole("main", { name: "Document" });
  await expect(documentLandmark).toBeVisible();
  await expect(page.getByRole("complementary", { name: "Comments" })).toBeVisible();
  const themeControl = page.getByRole("group", { name: "Theme" });
  await expect(themeControl).toBeVisible();
  const themeBounds = await themeControl.boundingBox();
  if (!themeBounds) {
    throw new Error("theme control has no visible bounds");
  }
  expect(1280 - (themeBounds.x + themeBounds.width)).toBeCloseTo(16, 0);
  expect(800 - (themeBounds.y + themeBounds.height)).toBeCloseTo(16, 0);
  await expect(page.locator("html")).toHaveAttribute("data-theme", "system");
  await expect(page).toHaveTitle("README.md — mdReview");

  await page.keyboard.press("Tab");
  const skipLink = page.getByRole("link", { name: "Skip to document" });
  await expect(skipLink).toBeFocused();
  await skipLink.press("Enter");
  await expect(documentLandmark).toBeFocused();

  const darkTheme = page.getByRole("button", { name: "Dark" });
  await darkTheme.click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  expect(await page.evaluate(() => localStorage.getItem("mdreview.theme"))).toBe("dark");
  await expectNoSeriousAccessibilityViolations(page);

  // Revisit the normal loopback URL and retain the saved theme.
  await page.goto(`/`);
  await expect(page.getByRole("heading", { level: 1, name: "Rich GFM document" })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.getByRole("button", { name: "Dark" })).toHaveAttribute("aria-pressed", "true");

  const nextDocument = page.getByRole("button", { name: /NEXT\.md/u });
  await nextDocument.focus();
  await nextDocument.press("Enter");
  await expect(page.getByRole("heading", { level: 1, name: "Next document" })).toBeVisible();
  await expect(page).toHaveTitle("NEXT.md — mdReview");
  await expect(documentLandmark).toBeFocused();

  const internalLink = page.getByRole("link", { name: "Return to the rich document" });
  await internalLink.focus();
  await internalLink.press("Enter");
  await expect(page.getByRole("heading", { level: 1, name: "Rich GFM document" })).toBeVisible();
  await expect(page).toHaveTitle("README.md — mdReview");
  await expect(documentLandmark).toBeFocused();
});

test.describe("motion preference", () => {
  test("keeps document and thread programmatic scrolling instant", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.addInitScript(() => {
      const scrollCalls: RecordedScroll[] = [];
      // The spy forwards with `call` so the native method keeps the element
      // receiver whose programmatic scroll behaviour is under test.
      // eslint-disable-next-line @typescript-eslint/unbound-method
      const originalScrollIntoView = Element.prototype.scrollIntoView;
      Element.prototype.scrollIntoView = function (
        options?: boolean | ScrollIntoViewOptions
      ): void {
        if (typeof options === "object") {
          scrollCalls.push({
            behavior: options.behavior,
            block: options.block
          });
        }
        originalScrollIntoView.call(this, options);
      };
      Object.defineProperty(window, "__mdreviewM4ScrollCalls", {
        value: scrollCalls
      });
    });

    await openRichDocument(page, reviewThreads());
    expect(await page.evaluate(() => matchMedia("(prefers-reduced-motion: reduce)").matches)).toBe(
      true
    );
    await page.evaluate(() => {
      (
        window as unknown as {
          __mdreviewM4ScrollCalls: RecordedScroll[];
        }
      ).__mdreviewM4ScrollCalls.length = 0;
    });

    await page.locator('.thread-card[data-thread-id="thread_m4_attached"] .thread-target').click();
    await expect
      .poll(() =>
        page.evaluate(() =>
          (
            window as unknown as {
              __mdreviewM4ScrollCalls: RecordedScroll[];
            }
          ).__mdreviewM4ScrollCalls.some(
            (call) => call.block === "center" && call.behavior === "auto"
          )
        )
      )
      .toBe(true);

    await page.getByRole("link", { name: "Next document" }).click();
    await expect(page.getByRole("heading", { level: 1, name: "Next document" })).toBeVisible();
    await expect
      .poll(() =>
        page.evaluate(() =>
          (
            window as unknown as {
              __mdreviewM4ScrollCalls: RecordedScroll[];
            }
          ).__mdreviewM4ScrollCalls.some((call) => call.block === "start")
        )
      )
      .toBe(true);
    const calls = await page.evaluate(
      () =>
        (
          window as unknown as {
            __mdreviewM4ScrollCalls: RecordedScroll[];
          }
        ).__mdreviewM4ScrollCalls
    );
    expect(calls.every((call) => call.behavior !== "smooth")).toBe(true);
  });
});

test("keeps forced-colour boundaries and controls operable when emulation is available", async ({
  page
}, testInfo) => {
  let forcedColoursEmulated = true;
  try {
    await page.emulateMedia({ forcedColors: "active" });
  } catch (error: unknown) {
    if (testInfo.project.name !== "firefox") {
      throw error;
    }
    forcedColoursEmulated = false;
    await page.emulateMedia({ contrast: "more" });
  }

  await openRichDocument(page, reviewThreads());
  const forcedColoursActive = await page.evaluate(
    () => matchMedia("(forced-colors: active)").matches
  );
  expect(forcedColoursActive).toBe(forcedColoursEmulated);

  const lightTheme = page.getByRole("button", { name: "Light" });
  await lightTheme.focus();
  await expect(lightTheme).toBeFocused();
  const themeFocus = await lightTheme.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      outlineStyle: style.outlineStyle,
      outlineWidth: Number.parseFloat(style.outlineWidth)
    };
  });
  expect(themeFocus.outlineStyle).toBe("solid");
  expect(themeFocus.outlineWidth).toBeGreaterThan(0);

  const attachedTarget = page.locator(
    '.thread-card[data-thread-id="thread_m4_attached"] .thread-target'
  );
  await attachedTarget.focus();
  await attachedTarget.press("Enter");
  const activeCard = page.locator(
    '.thread-card[data-thread-id="thread_m4_attached"][data-active="true"]'
  );
  await expect(activeCard).toBeVisible();
  const detachedCard = page.locator(
    '.thread-card[data-thread-id="thread_m4_detached"][data-attachment="detached"]'
  );
  await expect(detachedCard).toContainText("Detached");
  expect(await detachedCard.evaluate((element) => getComputedStyle(element).borderStyle)).toBe(
    "dashed"
  );
  await expect(page.getByRole("img", { name: "Image: Architecture diagram" })).toBeVisible();

  if (forcedColoursActive) {
    const activeBoundary = await activeCard.evaluate((element) => {
      const style = getComputedStyle(element);
      return {
        outlineStyle: style.outlineStyle,
        outlineWidth: Number.parseFloat(style.outlineWidth)
      };
    });
    expect(activeBoundary.outlineStyle).toBe("solid");
    expect(activeBoundary.outlineWidth).toBeGreaterThan(0);

    const activeHighlight = page.locator(".review-highlight.is-active").first();
    await expect(activeHighlight).toBeVisible();
    expect(
      await activeHighlight.evaluate((element) => getComputedStyle(element).borderTopStyle)
    ).toBe("solid");
  } else {
    const hasForcedColourRules = await page.evaluate(() =>
      Array.from(document.styleSheets).some((styleSheet) =>
        Array.from(styleSheet.cssRules).some((rule) =>
          rule.cssText.includes("(forced-colors: active)")
        )
      )
    );
    expect(hasForcedColourRules).toBe(true);
    await detachedCard.locator(".thread-target").focus();
    await expect(detachedCard.locator(".thread-target")).toBeFocused();
  }
});

test("keeps the fixed layout keyboard-operable at high zoom", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 700 });
  await openRichDocument(page, reviewThreads());

  const zoomApplied = await page.evaluate(() => {
    document.documentElement.style.zoom = "2";
    return getComputedStyle(document.documentElement).zoom === "2";
  });
  if (!zoomApplied) {
    await page.evaluate(() => {
      document.documentElement.style.removeProperty("zoom");
    });
    await page.setViewportSize({ width: 450, height: 350 });
  }

  const geometry = await page.evaluate(() => {
    const shell = document.querySelector<HTMLElement>(".app-shell");
    const files = document.querySelector<HTMLElement>(".files-panel");
    const documentPanel = document.querySelector<HTMLElement>(".document-panel");
    const review = document.querySelector<HTMLElement>(".review-panel");
    if (!shell || !files || !documentPanel || !review) {
      throw new Error("fixed layout is unavailable");
    }
    return {
      shell: shell.offsetWidth,
      files: files.offsetWidth,
      document: documentPanel.offsetWidth,
      review: review.offsetWidth,
      page: document.documentElement.scrollWidth,
      viewport: window.innerWidth
    };
  });
  expect(geometry).toMatchObject({
    shell: 1000,
    files: 240,
    document: 400,
    review: 360
  });
  expect(geometry.page).toBeGreaterThan(geometry.viewport);

  await page.evaluate(() => {
    window.scrollTo(0, 0);
    if (document.activeElement instanceof HTMLElement) {
      document.activeElement.blur();
    }
  });
  await page.keyboard.press("Tab");
  const skipLink = page.getByRole("link", { name: "Skip to document" });
  await expect(skipLink).toBeFocused();
  const lightTheme = page.getByRole("button", { name: "Light" });
  await lightTheme.focus();
  await expect(lightTheme).toBeFocused();
  expect(await lightTheme.evaluate((element) => element.closest(".panel") === null)).toBe(true);

  await skipLink.focus();
  await expect(skipLink).toBeFocused();
  await skipLink.press("Enter");
  await expect(page.getByRole("main", { name: "Document" })).toBeFocused();

  let reachedReview = false;
  for (let tabNumber = 0; tabNumber < 24; tabNumber += 1) {
    await page.keyboard.press("Tab");
    reachedReview = await page.evaluate(
      () => document.querySelector(".review-panel")?.contains(document.activeElement) ?? false
    );
    if (reachedReview) {
      break;
    }
  }
  expect(reachedReview).toBe(true);
  expect(await page.evaluate(() => window.scrollX)).toBeGreaterThan(0);
});
