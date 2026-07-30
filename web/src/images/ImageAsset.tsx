import { h } from "preact";
import { useState } from "preact/hooks";

function imageLabel(alt: string): string {
  return alt.trim() === "" ? "Image" : `Image: ${alt}`;
}

function assetURL(documentPath: string, reference: string): string {
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
