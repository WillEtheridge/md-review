import { h } from "preact";
import { useState } from "preact/hooks";

function imageLabel(alt: string): string {
  return alt.trim() === "" ? "Image" : `Image: ${alt}`;
}

function assetURL(documentPath: string, reference: string): string {
  // The renderer has already limited reference to a safe relative value; query
  // encoding preserves it as data for the server to validate again.
  return `/api/asset?${new URLSearchParams({ documentPath, reference }).toString()}`;
}

export function ImageAsset({
  documentPath,
  reference,
  alt,
  title
}: {
  documentPath: string;
  reference: string;
  alt: string;
  title?: string;
}) {
  // Images are lazy and same-origin. A failed request becomes an accessible
  // placeholder instead of retrying indefinitely or exposing the raw URL.
  const source = assetURL(documentPath, reference);
  const [failedSource, setFailedSource] = useState<string | null>(null);
  const label = imageLabel(alt);
  if (failedSource === source) {
    return h(
      "figure",
      {
        class: "markdown-media-placeholder markdown-image-placeholder"
      },
      h("span", { role: "img", "aria-label": label }, "Image could not be loaded.")
    );
  }

  return h(
    "figure",
    {
      class: "markdown-image"
    },
    h("img", {
      src: source,
      alt,
      ...(title ? { title } : {}),
      loading: "lazy",
      decoding: "async",
      onError: () => {
        setFailedSource(source);
      }
    })
  );
}
