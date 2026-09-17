package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/internal/shellruntime"
)

func newShellStorageTestProxy(t *testing.T) (*mekugiProxy, string) {
	t.Helper()
	proxy := &mekugiProxy{
		shellDirectory: t.TempDir(),
		shellSessions:  make(map[string]*shellSession),
		registry:       &toolRegistry{shellRuntime: "/unused-test-worker"},
	}
	t.Cleanup(func() { _ = proxy.Close() })
	directory, err := proxy.storeShellRuntime("thread-id")
	if err != nil {
		t.Fatal(err)
	}
	return proxy, directory
}

func testShellRuntimePath(t *testing.T, root, threadID string) string {
	t.Helper()
	path, err := shellruntime.Path(root, threadID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestShellStorageRetriesAfterDescriptorExhaustion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShellStorageOpenFailureProcess$", "-test.v")
	command.Env = append(os.Environ(), "MEKUGI_SHELL_STORAGE_OPEN_FAILURE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("storage open failure subprocess: %v\n%s", err, output)
	}
}

func TestShellStorageOpenFailureProcess(t *testing.T) {
	if os.Getenv("MEKUGI_SHELL_STORAGE_OPEN_FAILURE") != "1" {
		return
	}
	parent, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = min(limited.Cur, 96)
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limited); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
			t.Error(err)
		}
	}()
	var files []*os.File
	closeFiles := func() {
		for _, file := range files {
			_ = file.Close()
		}
		files = nil
	}
	defer closeFiles()
	for {
		file, err := os.Open(os.DevNull)
		if errors.Is(err, syscall.EMFILE) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	session := &shellSession{parent: parent, name: "scripts"}
	if err := session.createStorage(); !errors.Is(err, syscall.EMFILE) {
		t.Fatalf("storage open error = %v, want original EMFILE", err)
	}
	if session.scripts != nil {
		t.Fatal("failed open retained a storage capability")
	}
	if _, err := parent.Lstat(session.name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed open left an entry blocking retry: %v", err)
	}
	closeFiles()
	if err := session.createStorage(); err != nil {
		t.Fatalf("storage retry after releasing descriptors: %v", err)
	}
	if err := session.retireStorage(); err != nil {
		t.Fatal(err)
	}
}

func shellDescriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("descriptor enumeration unavailable: %v", err)
	}
	return len(entries)
}

func TestShellRuntimeHistoricalThreadsDoNotRetainDescriptors(t *testing.T) {
	proxy, _ := newShellStorageTestProxy(t)
	before := shellDescriptorCount(t)
	for i := range 80 {
		if _, err := proxy.storeShellRuntime(fmt.Sprintf("thread-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if growth := shellDescriptorCount(t) - before; growth > 2 {
		t.Fatalf("80 launcher-only threads retained %d descriptors", growth)
	}
}

func TestShellExpiredThreadsReleaseDescriptors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, _ := newShellStorageTestProxy(t)
		before := shellDescriptorCount(t)
		for i := range 32 {
			directory, err := proxy.storeShellRuntime(fmt.Sprintf("thread-%d", i))
			if err != nil {
				t.Fatal(err)
			}
			if !proxy.storeShellState(directory, "script", "checkpoint") {
				t.Fatal("retention failed")
			}
		}
		time.Sleep(shellArtifactTTL + time.Second)
		synctest.Wait()
		if growth := shellDescriptorCount(t) - before; growth > 2 {
			t.Fatalf("32 expired threads retained %d descriptors", growth)
		}
	})
}

func TestShellStorageLeasesDelayExpiryAndShutdown(t *testing.T) {
	for _, mode := range []string{"expiry", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				proxy, directory := newShellStorageTestProxy(t)
				if !proxy.storeShellState(directory, "script", "checkpoint") {
					t.Fatal("retention failed")
				}
				root, release, err := proxy.shellRoot(directory)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				_, releaseSecond, err := proxy.shellRoot(directory)
				if err != nil {
					t.Fatal(err)
				}
				defer releaseSecond()
				var closed chan error
				if mode == "expiry" {
					time.Sleep(shellArtifactTTL + time.Second)
				} else {
					closed = make(chan error, 1)
					go func() { closed <- proxy.Close() }()
				}
				synctest.Wait()
				release()
				release() // A lease release is idempotent.
				if _, err := root.Stat("script"); err != nil {
					t.Fatalf("active lease lost its artifact: %v", err)
				}
				select {
				case err := <-closed:
					t.Fatalf("shutdown passed an active lease: %v", err)
				default:
				}
				releaseSecond()
				synctest.Wait()
				if _, err := root.Stat("."); err == nil {
					t.Fatal("last lease did not close retired roots")
				}
				if mode == "expiry" {
					if _, err := os.Stat(filepath.Join(directory, "script")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("expired artifact survived last lease: %v", err)
					}
					if _, err := proxy.storeShellRuntime("thread-id"); err != nil {
						t.Fatalf("launcher could not be refreshed after expiry: %v", err)
					}
					if !proxy.storeShellState(directory, "next", "next checkpoint") {
						t.Fatal("retention could not reacquire idle storage")
					}
				} else if err := <-closed; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestShellShutdownRemovesLauncherWithoutScripts(t *testing.T) {
	proxy, directory := newShellStorageTestProxy(t)
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launcher preparation created script storage: %v", err)
	}
	runtimePath := testShellRuntimePath(t, proxy.shellDirectory, "thread-id")
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(runtimePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launcher survived shutdown without scripts: %v", err)
	}
}

func TestIdleShellStorageRejectsUnexpectedDirectories(t *testing.T) {
	for _, retired := range []bool{false, true} {
		for _, replacement := range []string{"directory", "symlink"} {
			t.Run(fmt.Sprintf("retired=%v/%s", retired, replacement), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					proxy, directory := newShellStorageTestProxy(t)
					if retired {
						if !proxy.storeShellState(directory, "old", "old checkpoint") {
							t.Fatal("retention failed")
						}
						time.Sleep(shellArtifactTTL + time.Second)
						synctest.Wait()
						if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("idle directory survived retirement: %v", err)
						}
					}
					// The old directory has been unlinked and its handles closed. Reject
					// this new directory whether or not the filesystem recycles its inode.
					target := directory
					if replacement == "symlink" {
						target = t.TempDir()
						if err := os.Symlink(target, directory); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Mkdir(directory, 0o700); err != nil {
						t.Fatal(err)
					}
					sentinel := filepath.Join(target, "sentinel")
					if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
						t.Fatal(err)
					}
					if proxy.storeShellState(directory, "script", "changed checkpoint") {
						t.Fatal("retention acquired unexpected storage")
					}
					if _, _, err := proxy.shellRoot(directory); err == nil {
						t.Fatal("lease acquired unexpected storage")
					}
					if _, err := proxy.storeShellRuntime("thread-id"); err != nil {
						t.Fatalf("launcher depended on script storage: %v", err)
					}
					if err := proxy.Close(); err != nil {
						t.Fatal(err)
					}
					if got, err := os.ReadFile(sentinel); err != nil || string(got) != "untouched" {
						t.Fatalf("cleanup touched replacement: %q, %v", got, err)
					}
					if _, err := os.Lstat(testShellRuntimePath(t, proxy.shellDirectory, "thread-id")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("owned launcher survived replacement scripts: %v", err)
					}
				})
			})
		}
	}
}

func TestShellRetentionRejectsUnsafeCallIDsAndExistingFiles(t *testing.T) {
	proxy, directory := newShellStorageTestProxy(t)
	if !proxy.storeShellState(directory, "seed", "private") {
		t.Fatal("retention failed")
	}
	outside := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", ".", "..", ".runtime", "../outside", "a/b", `a\b`, "bad\x00id", outside, "linked"} {
		if proxy.storeShellState(directory, id, "overwritten") {
			t.Fatalf("stored unsafe ID %q", id)
		}
	}
	if !proxy.storeShellState(directory, "normal", "first") {
		t.Fatal("ordinary retention failed")
	}
	if proxy.storeShellState(directory, "normal", "second") {
		t.Fatal("duplicate ID overwrote retained content")
	}
	for path, want := range map[string]string{outside: "untouched", filepath.Join(directory, "normal"): "first"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("content = %q, %v, want %q", got, err, want)
		}
	}
	if err := os.Rename(filepath.Join(directory, "normal"), filepath.Join(directory, "moved")); err != nil {
		t.Fatal(err)
	}
	if proxy.storeShellState(directory, "normal", "replacement") {
		t.Fatal("reused an ID while its original expiry callback is pending")
	}
	if _, err := os.Readlink(testShellRuntimePath(t, proxy.shellDirectory, "thread-id")); err != nil {
		t.Fatal(err)
	}
}

func TestShellRetentionExpiryAndCleanupStayInPinnedStorage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, directory := newShellStorageTestProxy(t)
		if !proxy.storeShellState(directory, "call-id", "checkpoint") {
			t.Fatal("retention failed")
		}
		thread := directory
		moved := thread + "-moved"
		if err := os.Rename(thread, moved); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "call-id")
		if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, thread); err != nil {
			t.Fatal(err)
		}
		time.Sleep(shellArtifactTTL + time.Second)
		synctest.Wait()
		if _, err := os.Stat(filepath.Join(moved, "call-id")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("original artifact survived expiry: %v", err)
		}
		if err := proxy.Close(); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(sentinel); err != nil || string(got) != "untouched" {
			t.Fatalf("escaped expiry or cleanup: %q, %v", got, err)
		}
	})
}

func TestShellRetentionShutdownPreservesReplacementDirectory(t *testing.T) {
	proxy, directory := newShellStorageTestProxy(t)
	if !proxy.storeShellState(directory, "call-id", "private checkpoint") {
		t.Fatal("retention failed")
	}
	thread := directory
	moved := thread + "-moved"
	if err := os.Rename(thread, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(thread, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(thread, "replacement")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "untouched" {
		t.Fatalf("replacement directory changed: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(moved, "call-id")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original script survived shutdown before expiry: %v", err)
	}
}

func TestMekugiTreatsShellArtifactLiteralAsContent(t *testing.T) {
	const script = "in /tmp/repro.txt\ntype 1:6db7 \"literal @shell/ marker\"\n"
	calls := 0
	translator := mekugiTranslatorFunc(func(_ context.Context, _ string, gotScript string) ([]byte, error) {
		calls++
		if gotScript != script {
			t.Fatalf("script = %q", gotScript)
		}
		return []byte(testTranslatedPatch), nil
	})
	transform, _, _, _ := newMekugiTestTransform(t, translator)
	history, err := transform.translate("call-edit", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || history.Applied {
		t.Fatalf("translations = %d, applied = %v", calls, history.Applied)
	}
}

func TestShellDoesNotRetainSource(t *testing.T) {
	for name, source := range map[string]string{
		"short":       "printf ok",
		"multiline":   "printf one\nprintf two\nprintf three\nprintf four\n",
		"interpreter": "#!python3\nprint('ok')\n",
		"batch":       "#!batch=NEXT\nprintf one\nNEXT\nprintf two\n",
	} {
		t.Run(name, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
			contribution, _ := proxy.registry.contribution("shell")
			history, err := transform.translateRegisteredTool(contribution, name, source, nil)
			if err != nil || history.TranslationError != "" {
				t.Fatalf("translate = %+v, %v", history, err)
			}
			var result map[string]json.RawMessage
			runShellCatJavaScript(t, proxy.registry.NodeExecutable, t.TempDir(), history.carrierInput(), &result,
				`tools.exec_command = async () => ({output:'ok',exit_code:0,future:'preserved'});`)
			for _, key := range []string{"retained", "retention", "script_ref"} {
				if _, exists := result[key]; exists {
					t.Fatalf("obsolete %s metadata: %s", key, mustMarshalJSON(result))
				}
			}
			if name != "batch" && (jsonString(result, "output") != "ok" || jsonString(result, "future") != "preserved") {
				t.Fatalf("native result fields lost: %s", mustMarshalJSON(result))
			}
			if _, err := os.Stat(transform.shellDirectory); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ordinary shell call created private storage: %v", err)
			}
		})
	}
}

func TestShellRejectsRemovedScriptDirective(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	contribution, _ := proxy.registry.contribution("shell")
	history, err := transform.translateRegisteredTool(contribution, "removed", "#!script=@shell/old\nprintf should-not-run", nil)
	if err != nil || !strings.Contains(history.TranslationError, "unsupported shell directive #!script") {
		t.Fatalf("removed directive = %+v, %v", history, err)
	}
	if strings.Contains(history.carrierInput(), "tools.exec_command") {
		t.Fatal("removed directive emitted an executable carrier")
	}
}
