import preact from "@preact/preset-vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [preact()],
  server: {
    host: "127.0.0.1",
    port: 41731,
    strictPort: true
  },
  preview: {
    host: "127.0.0.1",
    port: 41731,
    strictPort: true
  }
});
