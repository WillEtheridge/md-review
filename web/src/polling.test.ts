import { describe, expect, it, vi } from "vitest";

import { PollCoordinator, type PollClock, type VisibilitySource } from "./polling";

class FakeClock implements PollClock {
  nowMs = 0;
  nextHandle = 1;
  timers = new Map<number, { atMs: number; callback: () => void }>();

  now(): number {
    return this.nowMs;
  }

  setTimeout(callback: () => void, delayMs: number): number {
    const handle = this.nextHandle++;
    this.timers.set(handle, {
      atMs: this.nowMs + delayMs,
      callback
    });
    return handle;
  }

  clearTimeout(handle: number): void {
    this.timers.delete(handle);
  }

  advanceBy(delayMs: number): void {
    const targetMs = this.nowMs + delayMs;
    for (;;) {
      const next = [...this.timers.entries()]
        .filter(([, timer]) => timer.atMs <= targetMs)
        .sort((left, right) => left[1].atMs - right[1].atMs)
        .at(0);
      if (!next) {
        break;
      }
      this.nowMs = next[1].atMs;
      this.timers.delete(next[0]);
      next[1].callback();
    }
    this.nowMs = targetMs;
  }
}

class FakeVisibility implements VisibilitySource {
  visible: boolean;
  listeners = new Set<() => void>();

  constructor(visible: boolean) {
    this.visible = visible;
  }

  isVisible(): boolean {
    return this.visible;
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  setVisible(visible: boolean): void {
    this.visible = visible;
    for (const listener of this.listeners) {
      listener();
    }
  }
}

function deferred(): {
  promise: Promise<void>;
  resolve: () => void;
} {
  let resolvePromise: (() => void) | undefined;
  const promise = new Promise<void>((resolve) => {
    resolvePromise = resolve;
  });
  return {
    promise,
    resolve: () => {
      resolvePromise?.();
    }
  };
}

async function settle(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

describe("PollCoordinator", () => {
  it("starts immediately and keeps a one-second request-start cadence", async () => {
    const clock = new FakeClock();
    const visibility = new FakeVisibility(true);
    const starts: number[] = [];
    const coordinator = new PollCoordinator({
      clock,
      visibility,
      run: () => {
        starts.push(clock.now());
        return Promise.resolve();
      },
      onError: () => "continue"
    });

    coordinator.start();
    await settle();
    expect(starts).toEqual([0]);

    clock.advanceBy(999);
    await settle();
    expect(starts).toEqual([0]);

    clock.advanceBy(1);
    await settle();
    expect(starts).toEqual([0, 1000]);

    coordinator.stop();
    expect(clock.timers.size).toBe(0);
    expect(visibility.listeners.size).toBe(0);
  });

  it("does no hidden cadence work and requests immediately on return", async () => {
    const clock = new FakeClock();
    const visibility = new FakeVisibility(true);
    const starts: number[] = [];
    const coordinator = new PollCoordinator({
      clock,
      visibility,
      run: () => {
        starts.push(clock.now());
        return Promise.resolve();
      },
      onError: () => "continue"
    });

    coordinator.start();
    await settle();
    visibility.setVisible(false);
    clock.advanceBy(5000);
    await settle();
    expect(starts).toEqual([0]);
    expect(clock.timers.size).toBe(0);

    visibility.setVisible(true);
    await settle();
    expect(starts).toEqual([0, 5000]);
    coordinator.stop();
  });

  it("aborts hidden work and coalesces a returning trigger behind it", async () => {
    const clock = new FakeClock();
    const visibility = new FakeVisibility(true);
    const first = deferred();
    const signals: AbortSignal[] = [];
    let runs = 0;
    const coordinator = new PollCoordinator({
      clock,
      visibility,
      run: (signal) => {
        signals.push(signal);
        runs += 1;
        return runs === 1 ? first.promise : Promise.resolve();
      },
      onError: () => "continue"
    });

    coordinator.start();
    visibility.setVisible(false);
    expect(signals[0]?.aborted).toBe(true);
    visibility.setVisible(true);
    expect(runs).toBe(1);

    first.resolve();
    await settle();
    expect(runs).toBe(2);
    coordinator.stop();
  });

  it("never overlaps and collapses repeated triggers into one trailing cycle", async () => {
    const clock = new FakeClock();
    const visibility = new FakeVisibility(true);
    const first = deferred();
    const signals: AbortSignal[] = [];
    let active = 0;
    let maximumActive = 0;
    let runs = 0;
    const coordinator = new PollCoordinator({
      clock,
      visibility,
      run: async (signal) => {
        signals.push(signal);
        active += 1;
        maximumActive = Math.max(maximumActive, active);
        runs += 1;
        if (runs === 1) {
          await first.promise;
        }
        active -= 1;
      },
      onError: () => "continue"
    });

    coordinator.start();
    coordinator.requestNow();
    coordinator.requestNow();
    expect(signals[0]?.aborted).toBe(true);
    clock.advanceBy(5000);
    expect(runs).toBe(1);

    first.resolve();
    await settle();
    expect(runs).toBe(2);
    expect(maximumActive).toBe(1);
    coordinator.stop();
  });

  it("retries transient failures but stops and cleans up on a terminal decision", async () => {
    const clock = new FakeClock();
    const visibility = new FakeVisibility(true);
    const decisions = vi.fn<() => "continue" | "stop">();
    decisions.mockReturnValueOnce("continue").mockReturnValueOnce("stop");
    let runs = 0;
    const coordinator = new PollCoordinator({
      clock,
      visibility,
      run: () => {
        runs += 1;
        return Promise.reject(new Error(`failure ${String(runs)}`));
      },
      onError: decisions
    });

    coordinator.start();
    await settle();
    expect(runs).toBe(1);
    clock.advanceBy(1000);
    await settle();
    expect(runs).toBe(2);
    expect(decisions).toHaveBeenCalledTimes(2);
    expect(clock.timers.size).toBe(0);
    expect(visibility.listeners.size).toBe(0);
  });

  it("performs the bootstrap request even when initially hidden", async () => {
    const clock = new FakeClock();
    const visibility = new FakeVisibility(false);
    const run = vi.fn<() => Promise<void>>().mockResolvedValue();
    const coordinator = new PollCoordinator({
      clock,
      visibility,
      run,
      onError: () => "continue"
    });

    coordinator.start();
    await settle();
    expect(run).toHaveBeenCalledTimes(1);
    expect(clock.timers.size).toBe(0);
    coordinator.stop();
  });
});
