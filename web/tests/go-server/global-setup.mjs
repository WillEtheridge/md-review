import { spawn } from "node:child_process";
import { once } from "node:events";
import { cp, mkdir, mkdtemp, open, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { isAbsolute, join } from "node:path";
import process from "node:process";
import { clearTimeout, setTimeout } from "node:timers";
import { fileURLToPath, URL } from "node:url";

const projectDirectory = fileURLToPath(new URL("../../../", import.meta.url));
const configuredBinaryPath = process.env.MDREVIEW_GO_SERVER_BINARY;
if (configuredBinaryPath !== undefined && !isAbsolute(configuredBinaryPath)) {
  throw new Error("MDREVIEW_GO_SERVER_BINARY must be an absolute path");
}
const binaryPath = configuredBinaryPath ?? join(projectDirectory, "build", "mdreview");
const readOnlyFixturePath = join(projectDirectory, "testdata", "integration", "m1");
const reviewFixturePath = join(projectDirectory, "testdata", "integration", "m2");
const completeReviewFixturePath = join(projectDirectory, "testdata", "integration", "m3");
const readingExperienceFixturePath = join(projectDirectory, "testdata", "integration", "m4");

function environmentWithRuntime(runtimeDirectory) {
  const environment = { ...process.env };
  environment.PATH = "/mdreview-browser-no-executables";
  environment.XDG_RUNTIME_DIR = runtimeDirectory;
  return environment;
}

function waitForStartup(child) {
  return new Promise((resolve, reject) => {
    let pending = "";
    let output = "";
    let errorOutput = "";
    let instanceURL = "";

    const finish = (callback) => {
      clearTimeout(timeout);
      child.off("exit", handleExit);
      child.stdout.off("data", handleStdout);
      child.stderr.off("data", handleStderr);
      callback();
    };
    const handleExit = (code, signal) => {
      finish(() => {
        reject(
          new Error(
            `mdReview exited before readiness (code ${String(code)}, signal ${String(signal)})\n${errorOutput}`
          )
        );
      });
    };
    const handleStderr = (chunk) => {
      errorOutput += chunk;
    };
    const handleStdout = (chunk) => {
      output += chunk;
      pending += chunk;
      const lines = pending.split("\n");
      pending = lines.pop() ?? "";
      for (const line of lines) {
        if (line.startsWith("URL:")) {
          instanceURL = line.slice("URL:".length).trim();
        }
        if (line.includes("Press Ctrl+C to stop.")) {
          finish(() => {
            if (instanceURL.length === 0) {
              reject(new Error(`mdReview printed no startup URL\n${output}`));
              return;
            }
            resolve(instanceURL);
          });
          return;
        }
      }
    };
    const timeout = setTimeout(() => {
      finish(() => {
        reject(new Error(`timed out waiting for mdReview startup\n${output}\n${errorOutput}`));
      });
    }, 10_000);

    child.once("exit", handleExit);
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", handleStdout);
    child.stderr.on("data", handleStderr);
  });
}

async function stopChild(child) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return;
  }
  const gracefulExit = waitForExit(child, 5_000);
  child.kill("SIGINT");
  await gracefulExit;
  if (child.exitCode === null && child.signalCode === null) {
    const forcedExit = once(child, "exit");
    child.kill("SIGKILL");
    await forcedExit;
  }
}

function waitForExit(child, timeoutMilliseconds) {
  return new Promise((resolve) => {
    const handleExit = () => {
      clearTimeout(timeout);
      resolve();
    };
    const timeout = setTimeout(() => {
      child.off("exit", handleExit);
      resolve();
    }, timeoutMilliseconds);
    child.once("exit", handleExit);
  });
}

export default async function globalSetup() {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), "mdreview-browser-"));
  const workspaceDirectory = join(temporaryDirectory, "workspace");
  const runtimeDirectory = join(temporaryDirectory, "runtime");
  await cp(readOnlyFixturePath, workspaceDirectory, { recursive: true });
  await cp(reviewFixturePath, workspaceDirectory, { recursive: true });
  await cp(completeReviewFixturePath, workspaceDirectory, { recursive: true });
  await cp(readingExperienceFixturePath, workspaceDirectory, { recursive: true });
  await mkdir(runtimeDirectory, { mode: 0o700 });
  await writeFile(join(workspaceDirectory, "invalid.md"), Uint8Array.from([0xff, 0xfe]));

  const largeDocument = await open(join(workspaceDirectory, "large.md"), "w", 0o600);
  await largeDocument.truncate(8 * 1024 * 1024 + 1);
  await largeDocument.close();
  const largeReview = await open(
    join(workspaceDirectory, "large-review.md.review.json"),
    "w",
    0o600
  );
  await largeReview.truncate(8 * 1024 * 1024 + 1);
  await largeReview.close();

  const outsideDocument = join(temporaryDirectory, "outside-secret.md");
  await writeFile(outsideDocument, "outside secret\n", { mode: 0o600 });
  await symlink(outsideDocument, join(workspaceDirectory, "escape.md"));
  const outsideReview = join(temporaryDirectory, "outside-review.json");
  await writeFile(outsideReview, '{"schemaVersion":1,"threads":[]}\n', { mode: 0o600 });
  await symlink(outsideReview, join(workspaceDirectory, "unsafe-review.md.review.json"));

  const child = spawn(binaryPath, [workspaceDirectory], {
    env: environmentWithRuntime(runtimeDirectory),
    stdio: ["ignore", "pipe", "pipe"]
  });

  try {
    const instanceURL = await waitForStartup(child);
    const parsed = new URL(instanceURL);
    if (parsed.search || parsed.hash) {
      throw new Error("mdReview startup URL must not contain a query or fragment");
    }
    process.env.MDREVIEW_GO_SERVER_URL = parsed.toString().replace(/\/$/u, "");
    process.env.MDREVIEW_GO_SERVER_WORKSPACE = workspaceDirectory;
  } catch (error) {
    await stopChild(child);
    await rm(temporaryDirectory, { force: true, recursive: true });
    throw error;
  }

  return async () => {
    await stopChild(child);
    await rm(temporaryDirectory, { force: true, recursive: true });
  };
}
