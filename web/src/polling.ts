export interface PollClock {
  now(): number;
  setTimeout(callback: () => void, delayMs: number): number;
  clearTimeout(handle: number): void;
}

export interface VisibilitySource {
  isVisible(): boolean;
  subscribe(listener: () => void): () => void;
}

export type PollErrorDecision = "continue" | "stop";

export interface PollCoordinatorOptions {
  clock: PollClock;
  visibility: VisibilitySource;
  intervalMs?: number;
  run: (signal: AbortSignal) => Promise<void>;
  onError: (error: unknown) => PollErrorDecision;
}

const DEFAULT_INTERVAL_MS = 1000;

/**
 * PollCoordinator owns one visibility-aware request-start cadence. Triggers
 * that arrive during a cycle collapse into one trailing cycle, so state and
 * its document/review reconciliation never overlap.
 */
export class PollCoordinator {
  readonly #clock: PollClock;
  readonly #visibility: VisibilitySource;
  readonly #intervalMs: number;
  readonly #run: (signal: AbortSignal) => Promise<void>;
  readonly #onError: (error: unknown) => PollErrorDecision;

  #started = false;
  #stopped = false;
  #trailing = false;
  #timer: number | null = null;
  #active:
    | {
        controller: AbortController;
        startedAtMs: number;
      }
    | undefined;
  #unsubscribe: (() => void) | undefined;

  constructor(options: PollCoordinatorOptions) {
    this.#clock = options.clock;
    this.#visibility = options.visibility;
    this.#intervalMs = options.intervalMs ?? DEFAULT_INTERVAL_MS;
    this.#run = options.run;
    this.#onError = options.onError;

    if (!Number.isFinite(this.#intervalMs) || this.#intervalMs <= 0) {
      throw new TypeError("poll interval must be positive");
    }
  }

  start(): void {
    if (this.#started) {
      return;
    }
    this.#started = true;
    this.#unsubscribe = this.#visibility.subscribe(() => {
      this.#handleVisibilityChange();
    });

    // Bootstrap is immediate even when a newly created tab has not yet become
    // visible. Subsequent work follows Page Visibility strictly.
    this.#requestImmediately(true);
  }

  requestNow(): void {
    if (!this.#started || this.#stopped || !this.#visibility.isVisible()) {
      return;
    }
    // User completion/cancellation must not let an in-flight response captured
    // before that transition commit. Abort it and coalesce one fresh cycle.
    this.#requestImmediately(false, true);
  }

  stop(): void {
    if (this.#stopped) {
      return;
    }
    this.#stopped = true;
    this.#trailing = false;
    this.#clearTimer();
    this.#active?.controller.abort();
    this.#unsubscribe?.();
    this.#unsubscribe = undefined;
  }

  #handleVisibilityChange(): void {
    if (this.#stopped) {
      return;
    }
    if (!this.#visibility.isVisible()) {
      this.#trailing = false;
      this.#clearTimer();
      this.#active?.controller.abort();
      return;
    }
    this.#requestImmediately(false);
  }

  #requestImmediately(allowHidden: boolean, restartActive = false): void {
    if (this.#stopped || (!allowHidden && !this.#visibility.isVisible())) {
      return;
    }
    this.#clearTimer();
    if (this.#active) {
      this.#trailing = true;
      if (restartActive) {
        this.#active.controller.abort();
      }
      return;
    }
    this.#startCycle();
  }

  #startCycle(): void {
    if (this.#stopped) {
      return;
    }
    const controller = new AbortController();
    const active = {
      controller,
      startedAtMs: this.#clock.now()
    };
    this.#active = active;

    void this.#run(controller.signal)
      .catch((error: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        if (this.#onError(error) === "stop") {
          this.stop();
        }
      })
      .finally(() => {
        if (this.#active !== active) {
          return;
        }
        this.#active = undefined;
        if (this.#stopped || !this.#visibility.isVisible()) {
          return;
        }
        if (this.#trailing) {
          this.#trailing = false;
          this.#startCycle();
          return;
        }
        const nextStartMs = active.startedAtMs + this.#intervalMs;
        const delayMs = Math.max(0, nextStartMs - this.#clock.now());
        this.#timer = this.#clock.setTimeout(() => {
          this.#timer = null;
          if (this.#visibility.isVisible()) {
            this.#startCycle();
          }
        }, delayMs);
      });
  }

  #clearTimer(): void {
    if (this.#timer === null) {
      return;
    }
    this.#clock.clearTimeout(this.#timer);
    this.#timer = null;
  }
}

export function browserPollClock(): PollClock {
  return {
    now: () => performance.now(),
    setTimeout: (callback, delayMs) => window.setTimeout(callback, delayMs),
    clearTimeout: (handle) => {
      window.clearTimeout(handle);
    }
  };
}

export function browserVisibilitySource(documentObject: Document): VisibilitySource {
  return {
    isVisible: () => documentObject.visibilityState === "visible",
    subscribe: (listener) => {
      documentObject.addEventListener("visibilitychange", listener);
      return () => {
        documentObject.removeEventListener("visibilitychange", listener);
      };
    }
  };
}
