//go:build linux

package filesystem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMutateFileCreatesNewAndRespectsUmask(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		oldUmask := unix.Umask(0o027)
		defer unix.Umask(oldUmask)

		callbackCalls := 0
		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"docs/review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 3},
			func(current []byte, exists bool) ([]byte, error) {
				callbackCalls++
				if exists || current != nil {
					t.Fatalf("new target callback received current = %q, exists = %t", current, exists)
				}
				return []byte("created"), nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if durability != DurabilityDurable || string(emitted) != "created" {
			t.Fatalf("result = %q, %v; want created, durable", emitted, durability)
		}
		if callbackCalls != 1 {
			t.Fatalf("callback calls = %d, want 1", callbackCalls)
		}
		target := filepath.Join(root, "docs", "review.json")
		assertFileBytes(t, target, []byte("created"))
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Fatalf("new permissions = %o, want umask-derived 640", got)
		}
		assertDirectoryNames(t, filepath.Dir(target), []string{"review.json"})
	})
}

func TestMutateFileReplacesExistingAndRetainsPermissions(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		target := filepath.Join(root, "review.json")
		if err := os.WriteFile(target, []byte("before"), 0o640); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)

		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 3},
			func(current []byte, exists bool) ([]byte, error) {
				if !exists || string(current) != "before" {
					t.Fatalf("callback current = %q, exists = %t", current, exists)
				}
				current[0] = 'X'
				return []byte("after"), nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if durability != DurabilityDurable || string(emitted) != "after" {
			t.Fatalf("result = %q, %v; want after, durable", emitted, durability)
		}
		assertFileBytes(t, target, []byte("after"))
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Fatalf("replacement permissions = %o, want 640", got)
		}
		assertDirectoryNames(t, root, []string{"review.json"})
	})
}

func TestMutateFileRejectsTraversalAndUnsafeTargets(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "symlink.json")); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(root, "fifo.json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "directory.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", filepath.Join(root, "socket.json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := listener.Close(); err != nil {
				t.Error(err)
			}
		})
		filesystem := openTestFS(t, root, mode)
		options := MutationOptions{MaxBytes: 1024, MaxAttempts: 1}
		callback := func([]byte, bool) ([]byte, error) {
			return []byte("replacement"), nil
		}

		for _, relativePath := range []string{
			"symlink.json",
			"fifo.json",
			"directory.json",
			"socket.json",
		} {
			emitted, durability, err := filesystem.MutateFile(
				context.Background(),
				relativePath,
				options,
				callback,
			)
			if !errors.Is(err, ErrUnsafeMutationTarget) {
				t.Errorf("MutateFile(%q) error = %v, want ErrUnsafeMutationTarget", relativePath, err)
			}
			if emitted != nil || durability != DurabilityUnknown {
				t.Errorf("MutateFile(%q) result = %q, %v on rejection", relativePath, emitted, durability)
			}
		}
		for _, relativePath := range []string{
			"../outside.json",
			"/tmp/outside.json",
			"./review.json",
			"docs/../review.json",
		} {
			_, _, err := filesystem.MutateFile(
				context.Background(),
				relativePath,
				options,
				callback,
			)
			if !errors.Is(err, ErrInvalidRelativePath) {
				t.Errorf("MutateFile(%q) error = %v, want ErrInvalidRelativePath", relativePath, err)
			}
		}
		if _, _, err := filesystem.MutateFile(
			context.Background(),
			"missing/review.json",
			options,
			callback,
		); !errors.Is(err, ErrMutationIO) {
			t.Fatalf("missing parent error = %v, want ErrMutationIO", err)
		}
		assertFileBytes(t, outside, []byte("outside"))
	})
}

func TestMutateFileRejectsParentReplacement(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := t.TempDir()
		parent := filepath.Join(root, "swap")
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		outsideTarget := filepath.Join(outside, "review.json")
		if err := os.WriteFile(outsideTarget, []byte("outside"), 0o644); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		filesystem.hooks.beforeOpen = func(relativePath string) {
			if relativePath != "swap" {
				return
			}
			if err := os.Rename(parent, filepath.Join(root, "original")); err != nil {
				panic(err)
			}
			if err := os.Symlink(outside, parent); err != nil {
				panic(err)
			}
			filesystem.hooks.beforeOpen = nil
		}

		_, durability, err := filesystem.MutateFile(
			context.Background(),
			"swap/review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
			func([]byte, bool) ([]byte, error) {
				return []byte("inside"), nil
			},
		)
		if !errors.Is(err, ErrUnsafeMutationTarget) {
			t.Fatalf("parent replacement error = %v, want ErrUnsafeMutationTarget", err)
		}
		if durability != DurabilityUnknown {
			t.Fatalf("parent replacement durability = %v, want unknown", durability)
		}
		assertFileBytes(t, outsideTarget, []byte("outside"))
	})
}

func TestMutateFileCarriesOpenedParentAcrossPathReplacement(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		outside := t.TempDir()
		parent := filepath.Join(root, "docs")
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		insideTarget := filepath.Join(parent, "review.json")
		if err := os.WriteFile(insideTarget, []byte("inside"), 0o640); err != nil {
			t.Fatal(err)
		}
		outsideTarget := filepath.Join(outside, "review.json")
		if err := os.WriteFile(outsideTarget, []byte("outside"), 0o640); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		filesystem.hooks.mutation.afterOpenParent = func() {
			if err := os.Rename(parent, filepath.Join(root, "original")); err != nil {
				panic(err)
			}
			if err := os.Symlink(outside, parent); err != nil {
				panic(err)
			}
		}

		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"docs/review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
			func(current []byte, exists bool) ([]byte, error) {
				if !exists || string(current) != "inside" {
					t.Fatalf("contained callback current = %q, exists = %t", current, exists)
				}
				return []byte("updated-inside"), nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if string(emitted) != "updated-inside" || durability != DurabilityDurable {
			t.Fatalf("contained result = %q, %v", emitted, durability)
		}
		assertFileBytes(
			t,
			filepath.Join(root, "original", "review.json"),
			[]byte("updated-inside"),
		)
		assertFileBytes(t, outsideTarget, []byte("outside"))
	})
}

func TestMutateFileRetriesExactDestinationChange(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		target := filepath.Join(root, "review.json")
		if err := os.WriteFile(target, []byte("initial"), 0o640); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		filesystem.hooks.mutation.beforeFinalRead = func(attempt int) {
			if attempt == 1 {
				if err := os.WriteFile(target, []byte("external"), 0o640); err != nil {
					panic(err)
				}
			}
		}
		callbackCalls := 0

		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 3},
			func(current []byte, exists bool) ([]byte, error) {
				callbackCalls++
				if !exists {
					t.Fatal("existing target reported missing")
				}
				return append(append([]byte(nil), current...), []byte("+ours")...), nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if callbackCalls != 2 {
			t.Fatalf("callback calls = %d, want 2", callbackCalls)
		}
		if durability != DurabilityDurable || string(emitted) != "external+ours" {
			t.Fatalf("result = %q, %v; want external+ours, durable", emitted, durability)
		}
		assertFileBytes(t, target, []byte("external+ours"))
		assertDirectoryNames(t, root, []string{"review.json"})
	})
}

func TestMutateFileConcurrentChangeExhaustsBoundedAttempts(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		target := filepath.Join(root, "review.json")
		if err := os.WriteFile(target, []byte("initial"), 0o640); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		filesystem.hooks.mutation.beforeFinalRead = func(attempt int) {
			if err := os.WriteFile(
				target,
				[]byte(fmt.Sprintf("external-%d", attempt)),
				0o640,
			); err != nil {
				panic(err)
			}
		}
		callbackCalls := 0

		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 2},
			func(current []byte, exists bool) ([]byte, error) {
				callbackCalls++
				return []byte("ours"), nil
			},
		)
		if !errors.Is(err, ErrMutationConflict) {
			t.Fatalf("exhaustion error = %v, want ErrMutationConflict", err)
		}
		if emitted != nil || durability != DurabilityUnknown {
			t.Fatalf("exhaustion result = %q, %v", emitted, durability)
		}
		if callbackCalls != 2 {
			t.Fatalf("callback calls = %d, want 2", callbackCalls)
		}
		assertFileBytes(t, target, []byte("external-2"))
		assertDirectoryNames(t, root, []string{"review.json"})
	})
}

func TestMutateFileTemporaryFailuresLeaveOriginalAndCleanUp(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		for _, test := range []struct {
			name   string
			inject func(*FS)
		}{
			{
				name: "write",
				inject: func(filesystem *FS) {
					filesystem.hooks.mutation.writeTemporary = func(
						attempt int,
						file *os.File,
						data []byte,
					) error {
						if _, err := file.Write(data[:1]); err != nil {
							return err
						}
						return errors.New("injected temporary write failure")
					}
				},
			},
			{
				name: "sync",
				inject: func(filesystem *FS) {
					filesystem.hooks.mutation.beforeTemporarySync = func(attempt int) error {
						return errors.New("injected temporary sync failure")
					}
				},
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				root := t.TempDir()
				target := filepath.Join(root, "review.json")
				if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
					t.Fatal(err)
				}
				filesystem := openTestFS(t, root, mode)
				test.inject(filesystem)

				emitted, durability, err := filesystem.MutateFile(
					context.Background(),
					"review.json",
					MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
					func([]byte, bool) ([]byte, error) {
						return []byte("updated"), nil
					},
				)
				if !errors.Is(err, ErrMutationIO) {
					t.Fatalf("injected %s error = %v, want ErrMutationIO", test.name, err)
				}
				if emitted != nil || durability != DurabilityUnknown {
					t.Fatalf("injected %s result = %q, %v", test.name, emitted, durability)
				}
				assertFileBytes(t, target, []byte("original"))
				assertDirectoryNames(t, root, []string{"review.json"})
			})
		}
	})
}

func TestMutateFileDirectorySyncFailureIsAppliedUncertain(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		target := filepath.Join(root, "review.json")
		if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		filesystem.hooks.mutation.beforeDirectorySync = func(attempt int) error {
			return errors.New("injected directory sync failure")
		}

		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 3},
			func([]byte, bool) ([]byte, error) {
				return []byte("updated"), nil
			},
		)
		if err != nil {
			t.Fatalf("uncertain applied result error = %v, want nil", err)
		}
		if string(emitted) != "updated" || durability != DurabilityUncertain {
			t.Fatalf("uncertain result = %q, %v", emitted, durability)
		}
		assertFileBytes(t, target, []byte("updated"))
		assertDirectoryNames(t, root, []string{"review.json"})
	})
}

func TestMutateFileRejectsOversizedInputAndOutput(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		for _, test := range []struct {
			name     string
			initial  []byte
			updated  []byte
			wantCall bool
		}{
			{name: "input", initial: []byte("four"), updated: []byte("ok"), wantCall: false},
			{name: "output", initial: []byte("ok"), updated: []byte("four"), wantCall: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				root := t.TempDir()
				target := filepath.Join(root, "review.json")
				if err := os.WriteFile(target, test.initial, 0o640); err != nil {
					t.Fatal(err)
				}
				filesystem := openTestFS(t, root, mode)
				callbackCalled := false

				emitted, durability, err := filesystem.MutateFile(
					context.Background(),
					"review.json",
					MutationOptions{MaxBytes: 3, MaxAttempts: 1},
					func([]byte, bool) ([]byte, error) {
						callbackCalled = true
						return test.updated, nil
					},
				)
				if !errors.Is(err, ErrMutationTooLarge) {
					t.Fatalf("%s limit error = %v, want ErrMutationTooLarge", test.name, err)
				}
				if callbackCalled != test.wantCall {
					t.Fatalf("%s callback called = %t, want %t", test.name, callbackCalled, test.wantCall)
				}
				if emitted != nil || durability != DurabilityUnknown {
					t.Fatalf("%s limit result = %q, %v", test.name, emitted, durability)
				}
				assertFileBytes(t, target, test.initial)
				assertDirectoryNames(t, root, []string{"review.json"})
			})
		}
	})
}

func TestMutateFileRejectsUnsafeDestinationAtFinalCheck(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		target := filepath.Join(root, "review.json")
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		filesystem.hooks.mutation.beforeFinalRead = func(attempt int) {
			if err := os.Remove(target); err != nil {
				panic(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				panic(err)
			}
		}

		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 2},
			func([]byte, bool) ([]byte, error) {
				return []byte("updated"), nil
			},
		)
		if !errors.Is(err, ErrUnsafeMutationTarget) {
			t.Fatalf("final unsafe error = %v, want ErrUnsafeMutationTarget", err)
		}
		if emitted != nil || durability != DurabilityUnknown {
			t.Fatalf("final unsafe result = %q, %v", emitted, durability)
		}
		assertFileBytes(t, outside, []byte("outside"))
		assertDirectoryNames(t, root, []string{"review.json"})
	})
}

func TestMutateFileRenameFailureCleansTemporary(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		target := filepath.Join(root, "review.json")
		if err := os.WriteFile(target, []byte("initial"), 0o640); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		filesystem.hooks.mutation.afterFinalCheck = func(attempt int) {
			if err := os.Remove(target); err != nil {
				panic(err)
			}
			if err := os.Mkdir(target, 0o755); err != nil {
				panic(err)
			}
		}

		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
			func([]byte, bool) ([]byte, error) {
				return []byte("updated"), nil
			},
		)
		if !errors.Is(err, ErrMutationIO) {
			t.Fatalf("rename failure error = %v, want ErrMutationIO", err)
		}
		if emitted != nil || durability != DurabilityUnknown {
			t.Fatalf("rename failure result = %q, %v", emitted, durability)
		}
		info, statErr := os.Stat(target)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !info.IsDir() {
			t.Fatalf("replacement race target mode = %v, want directory", info.Mode())
		}
		assertDirectoryNames(t, root, []string{"review.json"})
	})
}

func TestMutateFileFinalCheckToRenameExternalWriterLimitation(t *testing.T) {
	forEachResolutionMode(t, func(t *testing.T, mode ResolutionMode) {
		root := t.TempDir()
		target := filepath.Join(root, "review.json")
		if err := os.WriteFile(target, []byte("initial"), 0o640); err != nil {
			t.Fatal(err)
		}
		filesystem := openTestFS(t, root, mode)
		filesystem.hooks.mutation.afterFinalCheck = func(attempt int) {
			if err := os.WriteFile(target, []byte("external-after-check"), 0o640); err != nil {
				panic(err)
			}
		}

		emitted, durability, err := filesystem.MutateFile(
			context.Background(),
			"review.json",
			MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
			func([]byte, bool) ([]byte, error) {
				return []byte("ours"), nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if string(emitted) != "ours" || durability != DurabilityDurable {
			t.Fatalf("race-boundary result = %q, %v", emitted, durability)
		}
		assertFileBytes(t, target, []byte("ours"))
	})
}

func TestMutateFileOptionsContextAndCallbackErrors(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "review.json")
	if err := os.WriteFile(target, []byte("initial"), 0o640); err != nil {
		t.Fatal(err)
	}
	filesystem := openTestFS(t, root, Auto)
	callback := func([]byte, bool) ([]byte, error) {
		return []byte("updated"), nil
	}

	for _, options := range []MutationOptions{
		{MaxBytes: -1, MaxAttempts: 1},
		{MaxBytes: 1, MaxAttempts: -1},
		{MaxBytes: 1, MaxAttempts: MaxMutationAttempts + 1},
	} {
		if _, _, err := filesystem.MutateFile(
			context.Background(),
			"review.json",
			options,
			callback,
		); !errors.Is(err, ErrInvalidMutationOptions) {
			t.Errorf("options %+v error = %v, want ErrInvalidMutationOptions", options, err)
		}
	}
	if _, _, err := filesystem.MutateFile(
		context.Background(),
		"review.json",
		MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
		nil,
	); !errors.Is(err, ErrInvalidMutationOptions) {
		t.Fatalf("nil callback error = %v, want ErrInvalidMutationOptions", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := filesystem.MutateFile(
		ctx,
		"review.json",
		MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
		callback,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation error = %v, want context.Canceled", err)
	}

	callbackErr := errors.New("semantic callback rejected mutation")
	if _, _, err := filesystem.MutateFile(
		context.Background(),
		"review.json",
		MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
		func([]byte, bool) ([]byte, error) {
			return nil, callbackErr
		},
	); !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v, want callback sentinel", err)
	}
	assertFileBytes(t, target, []byte("initial"))
	assertDirectoryNames(t, root, []string{"review.json"})
}

func TestMutateFileCancellationAfterTemporarySyncCleansUp(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "review.json")
	if err := os.WriteFile(target, []byte("initial"), 0o640); err != nil {
		t.Fatal(err)
	}
	filesystem := openTestFS(t, root, Auto)
	ctx, cancel := context.WithCancel(context.Background())
	filesystem.hooks.mutation.beforeFinalRead = func(attempt int) {
		cancel()
	}

	emitted, durability, err := filesystem.MutateFile(
		ctx,
		"review.json",
		MutationOptions{MaxBytes: 1024, MaxAttempts: 1},
		func([]byte, bool) ([]byte, error) {
			return []byte("updated"), nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-mutation cancellation error = %v, want context.Canceled", err)
	}
	if emitted != nil || durability != DurabilityUnknown {
		t.Fatalf("mid-mutation cancellation result = %q, %v", emitted, durability)
	}
	assertFileBytes(t, target, []byte("initial"))
	assertDirectoryNames(t, root, []string{"review.json"})
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s bytes = %q, want %q", path, got, want)
	}
}

func assertDirectoryNames(t *testing.T, directory string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s entries = %v, want %v", directory, got, want)
	}
}
