import { h } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";

import type { ImageResourceManager, ImageResourceState } from "./manager";

const IMAGE_ROOT_MARGIN = "400px 0px";

function imageLabel(alt: string): string {
  return alt.trim() === "" ? "Image" : `Image: ${alt}`;
}

function errorMessage(state: Extract<ImageResourceState, { status: "error" }>): string {
  switch (state.kind) {
    case "missing":
      return "Image not found. Check the relative path.";
    case "unsupported":
      return "Unsupported image. Use PNG, JPEG, GIF, or WebP.";
    case "oversized":
      return "Image is larger than 20 MiB.";
    case "corrupt":
      return "Image data could not be displayed safely.";
    case "unavailable":
      return "Image could not be loaded.";
  }
}

export function ImageAsset({
  manager,
  observerRoot,
  reference,
  alt,
  title
}: {
  manager: ImageResourceManager;
  observerRoot: HTMLElement | null;
  reference: string;
  alt: string;
  title?: string;
}) {
  const container = useRef<HTMLElement>(null);
  const [state, setState] = useState<ImageResourceState>({
    status: "deferred",
    reason: "initial"
  });

  useEffect(() => {
    const nextSubscription = manager.subscribe(reference, setState);
    setState(nextSubscription.getState());

    const target = container.current;
    if (!target) {
      nextSubscription.setNearViewport(true);
      return () => {
        nextSubscription.unsubscribe();
      };
    }
    const root = observerRoot ?? target.closest<HTMLElement>(".document-panel");
    const observer = new IntersectionObserver(
      (entries) => {
        const entry = entries[0];
        if (entry) {
          nextSubscription.setNearViewport(entry.isIntersecting);
        }
      },
      {
        root,
        rootMargin: IMAGE_ROOT_MARGIN
      }
    );
    observer.observe(target);
    return () => {
      observer.disconnect();
      nextSubscription.setNearViewport(false);
      nextSubscription.unsubscribe();
    };
  }, [manager, observerRoot, reference]);

  if (state.status === "ready") {
    return h(
      "figure",
      {
        ref: container,
        class: "markdown-image",
        "data-image-state": "ready"
      },
      h("img", {
        src: state.objectURL,
        alt,
        ...(title ? { title } : {}),
        loading: "lazy",
        decoding: "async",
        onError: () => {
          manager.reportDecodeFailure(reference, state.objectURL);
        }
      })
    );
  }

  const label = imageLabel(alt);
  const statusText =
    state.status === "loading" || state.status === "queued"
      ? "Loading image…"
      : state.status === "error"
        ? errorMessage(state)
        : label;
  const action =
    state.status === "deferred"
      ? h(
          "button",
          {
            class: "image-action",
            type: "button",
            "aria-label": `Load ${label.toLowerCase()}`,
            onClick: () => {
              manager.retry(reference);
            }
          },
          "Load image"
        )
      : state.status === "error" && state.retryable
        ? h(
            "button",
            {
              class: "image-action",
              type: "button",
              "aria-label": `Retry ${label.toLowerCase()}`,
              onClick: () => {
                manager.retry(reference);
              }
            },
            "Retry"
          )
        : null;
  return h(
    "figure",
    {
      ref: container,
      class: "markdown-media-placeholder markdown-image-placeholder",
      "data-image-state": state.status
    },
    h("span", { role: "img", "aria-label": label }, statusText),
    action
  );
}
