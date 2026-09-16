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
			if _, _, retained := proxy.retainShell(directory, "script", "printf ok\n"); !retained {
				t.Fatal("retention failed")
			}
			if got, err := proxy.resolveShellInput(directory, "#!script=@shell/script"); err != nil || got != "printf ok\n" {
				t.Fatalf("live retained input = %q, %v", got, err)
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
				if _, _, retained := proxy.retainShell(directory, "script", "printf ok\n"); !retained {
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
					if _, _, retained := proxy.retainShell(directory, "next", "printf next\n"); !retained {
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
						if _, _, retained := proxy.retainShell(directory, "old", "old script"); !retained {
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
					if _, _, retained := proxy.retainShell(directory, "script", "printf changed"); retained {
						t.Fatal("retention acquired unexpected storage")
					}
					if _, err := proxy.resolveShellInput(directory, "#!script=@shell/sentinel"); err == nil {
						t.Fatal("read acquired unexpected storage")
					}
					if _, _, err := proxy.shellRoot(directory); err == nil {
						t.Fatal("Apply acquired unexpected storage")
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

type expiryCheckingShellTranslator struct {
	inProcessMekugiTranslator
	t      *testing.T
	expire func()
}

func (translator expiryCheckingShellTranslator) Apply(ctx context.Context, root *os.Root, script string) (mekugiTranslationResult, error) {
	translator.expire()
	if _, err := root.Stat("script"); err != nil {
		translator.t.Fatalf("expiry removed an in-flight Apply input: %v", err)
	}
	result, err := translator.inProcessMekugiTranslator.Apply(ctx, root, script)
	if err == nil {
		got, readErr := root.ReadFile("script")
		if readErr != nil || string(got) != "printf fixed\n" {
			translator.t.Fatalf("in-flight Apply result = %q, %v", got, readErr)
		}
	}
	return result, err
}

func TestRetainedShellApplyHoldsLeaseThroughExpiry(t *testing.T) {
	translator := &expiryCheckingShellTranslator{dataDirectory: t.TempDir(), t: t}
	transform, proxy, _, _ := newMekugiTestTransform(t, translator)
	if _, _, retained := proxy.retainShell(transform.shellDirectory, "script", "printf ok\n"); !retained {
		t.Fatal("retention failed")
	}
	translator.expire = func() {
		// Fire the existing expiry callback after Apply has acquired its lease.
		proxy.mu.Lock()
		session := proxy.shellSessions[transform.shellDirectory]
		session.timers["script"].Reset(0)
		proxy.mu.Unlock()
		deadline := time.Now().Add(2 * time.Second)
		for {
			proxy.mu.RLock()
			expired := session.timers["script"] == nil
			proxy.mu.RUnlock()
			if expired {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("expiry callback did not run during Apply")
			}
			time.Sleep(time.Millisecond)
		}
	}
	history, err := transform.translate("call-edit", "in @shell/script\ntype 1:ef86 \"printf fixed\"\n", nil)
	if err != nil || !history.Applied || history.TranslationError != "" {
		t.Fatalf("leased Apply = %+v, %v", history, err)
	}
	if _, err := os.Stat(filepath.Join(transform.shellDirectory, "script")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact survived expired Apply lease: %v", err)
	}
	proxy.mu.RLock()
	defer proxy.mu.RUnlock()
	if session := proxy.shellSessions[transform.shellDirectory]; session.leases != 0 || session.scripts != nil {
		t.Fatalf("Apply did not release idle storage: %+v", session)
	}
}

func TestShellRetentionRejectsUnsafeCallIDsAndExistingFiles(t *testing.T) {
	proxy, directory := newShellStorageTestProxy(t)
	if _, _, retained := proxy.retainShell(directory, "seed", "private"); !retained {
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
		if ref, _, retained := proxy.retainShell(directory, id, "overwritten"); retained || ref != "" {
			t.Fatalf("retained unsafe ID %q as %q", id, ref)
		}
	}
	if _, _, retained := proxy.retainShell(directory, "normal", "first"); !retained {
		t.Fatal("ordinary retention failed")
	}
	if _, _, retained := proxy.retainShell(directory, "normal", "second"); retained {
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
	if _, _, retained := proxy.retainShell(directory, "normal", "replacement"); retained {
		t.Fatal("reused an ID while its original expiry callback is pending")
	}
	if _, err := os.Readlink(testShellRuntimePath(t, proxy.shellDirectory, "thread-id")); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedShellResolverConfinesReferences(t *testing.T) {
	proxy, directory := newShellStorageTestProxy(t)
	const body = "#!python3\nprint('retained')\n"
	if _, _, ok := proxy.retainShell(directory, "original", body); !ok {
		t.Fatal("retention failed")
	}
	if _, _, ok := proxy.retainShell(directory, "nested", "#!script=@shell/original"); !ok {
		t.Fatal("nested retention failed")
	}
	for _, input := range []string{body, "#!script=@shell/original", "#!script=@shell/nested"} {
		got, err := proxy.resolveShellInput(directory, input)
		if err != nil || got != body {
			t.Fatalf("resolve %q = %q, %v", input, got, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := proxy.retainShell(directory, "cycle", "#!script=@shell/cycle"); !ok {
		t.Fatal("cycle fixture retention failed")
	}
	for _, reference := range []string{outside, "relative", "@shell/", "@shell/../outside", "@shell/../.runtime", "@shell/.runtime", "@shell/linked", "@shell/cycle", "@shell/missing", `@shell/a\b`} {
		if got, err := proxy.resolveShellInput(directory, "#!script="+reference); err == nil || got != "" {
			t.Fatalf("accepted %q: %q, %v", reference, got, err)
		}
	}
}

func TestShellRetentionExpiryAndCleanupStayInPinnedStorage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, directory := newShellStorageTestProxy(t)
		if _, _, ok := proxy.retainShell(directory, "call-id", "retained"); !ok {
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
	if _, _, ok := proxy.retainShell(directory, "call-id", "private script"); !ok {
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

func TestShellRerunPreservesOriginalHistoryAndRetainsResolvedBody(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	const body = "#!python3\nprint('retained')\n"
	if _, _, ok := proxy.retainShell(transform.shellDirectory, "original", body); !ok {
		t.Fatal("retention failed")
	}
	const input = "#!script=@shell/original"
	history, err := transform.translateTool("shell", "rerun", input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if history.TranslationError != "" || history.Script != input || !strings.Contains(history.carrierInput(), "retained") {
		t.Fatalf("rerun history: %+v", history)
	}
	if got, err := os.ReadFile(filepath.Join(transform.shellDirectory, "rerun")); err != nil || string(got) != body {
		t.Fatalf("retained rerun = %q, %v", got, err)
	}
	for index, reference := range []string{"/etc/passwd", "@shell/../.runtime", "@shell/missing"} {
		history, err := transform.translateTool("shell", fmt.Sprintf("rejected-%d", index), "#!script="+reference, nil)
		if err != nil || history.TranslationError == "" {
			t.Fatalf("reference %q did not produce a tool rejection: %+v, %v", reference, history, err)
		}
	}
}

func TestRetainedShellEditsCannotReachOutsideScripts(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	if _, _, retained := proxy.retainShell(transform.shellDirectory, "seed", "private"); !retained {
		t.Fatal("retention failed")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("printf ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(transform.shellDirectory, "linked")); err != nil {
		t.Fatal(err)
	}
	for index, reference := range []string{"@shell/../.runtime", "@shell/.runtime", "@shell/linked", "@shell/" + outside} {
		history, err := transform.translate(fmt.Sprintf("call-edit-%d", index), "in "+reference+"\ntype 1:ef86 \"changed\"\n", nil)
		if err != nil || history.TranslationError == "" || history.Applied {
			t.Fatalf("accepted retained escape %q: %+v, %v", reference, history, err)
		}
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "printf ok\n" {
		t.Fatalf("outside file changed: %q, %v", got, err)
	}
	if got, err := os.Readlink(testShellRuntimePath(t, proxy.shellDirectory, "thread-1")); err != nil || got != proxy.registry.shellRuntime {
		t.Fatalf("runtime launcher changed: %q, %v", got, err)
	}
}

func TestRetainedShellScheduledExpiryDoesNotRenewOnRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, directory := newShellStorageTestProxy(t)
		started := time.Now()
		reference, expiry, retained := proxy.retainShell(directory, "expiry", "printf ok\n")
		if !retained || !expiry.Equal(started.Add(shellArtifactTTL)) {
			t.Fatalf("expiry = %v, retained=%v", expiry, retained)
		}
		time.Sleep(shellArtifactTTL / 2)
		if source, err := proxy.resolveShellInput(directory, "#!script="+reference); err != nil || source != "printf ok\n" {
			t.Fatalf("read before expiry = %q, %v", source, err)
		}
		time.Sleep(shellArtifactTTL / 2)
		synctest.Wait()
		if _, err := proxy.resolveShellInput(directory, "#!script="+reference); err == nil {
			t.Fatal("reading renewed the original expiry")
		}
		if _, rejectedExpiry, retained := proxy.retainShell(directory, "..", "invalid"); retained || !rejectedExpiry.IsZero() {
			t.Fatalf("failed retention exposed an expiry: %v, %v", rejectedExpiry, retained)
		}
	})
}

func TestShellRetentionLifetimeMetadata(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	contribution, _ := proxy.registry.contribution("shell")
	before := time.Now().Add(shellArtifactTTL)
	history, err := transform.translateRegisteredTool(contribution, "lifetime", "#!python3\nprint('ok')\n", nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %+v, %v", history, err)
	}
	after := time.Now().Add(shellArtifactTTL)
	var result struct {
		Retained  bool   `json:"retained"`
		Reference string `json:"script_ref"`
		Retention struct {
			Scope    string    `json:"scope"`
			Durable  bool      `json:"durable"`
			Expiry   time.Time `json:"scheduled_expiry"`
			Shutdown bool      `json:"ends_on_router_shutdown"`
			Renewed  bool      `json:"reads_or_edits_extend_lifetime"`
		} `json:"retention"`
		Output string `json:"output"`
		Future string `json:"future"`
	}
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, t.TempDir(), history.carrierInput(), &result,
		`tools.exec_command = async () => ({output:'ok',exit_code:0,future:'preserved'});`)
	if !result.Retained || result.Reference != "@shell/lifetime" || result.Retention.Scope != "thread" ||
		result.Retention.Durable || !result.Retention.Shutdown || result.Retention.Renewed ||
		result.Retention.Expiry.Before(before) || result.Retention.Expiry.After(after) ||
		result.Output != "ok" || result.Future != "preserved" {
		t.Fatalf("lifetime metadata = %+v", result)
	}
	replayed, err := transform.translateRegisteredTool(contribution, "lifetime", "#!python3\nprint('ok')\n", nil)
	if err != nil || replayed.carrierInput() != history.carrierInput() {
		t.Fatal("replay changed the recorded expiry")
	}
}

func TestShellRetentionLifecycle(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "shell")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{shellDirectory: directory, shellSessions: make(map[string]*shellSession), registry: &toolRegistry{shellRuntime: "/unused-test-worker"}}
	const sessionID = "019fe9b0-c75b-7f92-9ce0-1580bca5e4ab"
	sessionDirectory, err := proxy.storeShellRuntime(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	originalTTL := shellArtifactTTL
	shellArtifactTTL = 10 * time.Millisecond
	t.Cleanup(func() { shellArtifactTTL = originalTTL })

	reference, _, retained := proxy.retainShell(sessionDirectory, "call-id", "printf ok\n")
	if !retained || reference != "@shell/call-id" {
		t.Fatalf("retention = %q, %v", reference, retained)
	}
	path := filepath.Join(sessionDirectory, "call-id")
	if content, err := os.ReadFile(path); err != nil || string(content) != "printf ok\n" {
		t.Fatalf("retained content = %q, %v", content, err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("retained script did not expire")
		}
		time.Sleep(time.Millisecond)
	}

	if _, _, retained := proxy.retainShell(sessionDirectory, "call-next", "printf next\n"); !retained {
		t.Fatal("second script was not retained")
	}
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sessionDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session directory survived close: %v", err)
	}
}

func TestRetainedShellEditWithoutActiveStorageIsRejected(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	history, err := transform.translate("call-missing", "in @shell/missing\ntype 1:ef86 \"fixed\"\n", nil)
	if err != nil || !history.Unevaluated || !strings.Contains(history.TranslationError, "retained shell storage is unavailable") {
		t.Fatalf("missing retained edit = %+v, %v", history, err)
	}
	if _, err := os.Stat(transform.shellDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing reference created storage: %v", err)
	}
}

func TestMekugiAppliesRetainedShellArtifactDirectly(t *testing.T) {
	dataDirectory := t.TempDir()
	outcomePath := filepath.Join(t.TempDir(), "outcome.txt")
	settings := `{"hooks":{"outcome":["printf '%s' {{.EmittedBytes}}'|'{{.EvaluatedBytes}} > ` + shellQuoteArgument(outcomePath) + `"]}}`
	if err := os.WriteFile(filepath.Join(dataDirectory, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	transform, proxy, _, _ := newMekugiTestTransform(
		t,
		newInProcessMekugiTranslator(dataDirectory),
	)
	reference, _, retained := proxy.retainShell(transform.shellDirectory, "call-shell", "printf ok\n")
	if !retained {
		t.Fatal("shell script was not retained")
	}
	emitted := "in " + reference + "\ntype 1:ef86 \"printf @shell/fixed\"\n"
	evaluated := "in call-shell\ntype 1:ef86 \"printf @shell/fixed\"\n"
	history, err := transform.translate("call-edit", emitted, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(transform.shellDirectory, "call-shell")
	if content, err := os.ReadFile(path); err != nil || string(content) != "printf @shell/fixed\n" {
		t.Fatalf("applied content = %q, %v", content, err)
	}
	if !history.Applied || history.Patch != "" || strings.Contains(history.carrierInput(), "apply_patch") || strings.Contains(history.carrierInput(), "exec_command") {
		t.Fatalf("retained edit used host patch carrier: %+v, %s", history, history.carrierInput())
	}
	got, err := os.ReadFile(outcomePath)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%d|%d", len(emitted), len(evaluated))
	if string(got) != want {
		t.Fatalf("outcome byte counts = %q, want %q", got, want)
	}
}

func TestMekugiRecoveryAppliesRetainedShellArtifactDirectly(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	reference, _, retained := proxy.retainShell(transform.shellDirectory, "call-shell", "printf ok\n")
	if !retained {
		t.Fatal("shell script was not retained")
	}
	emitted := "in " + reference + "\ntype 1:aaaa \"printf fixed\"\n"
	first, err := transform.translate("call-edit", emitted, nil)
	if err != nil || !first.EvaluatorRejected {
		t.Fatalf("initial rejection = %+v, %v", first, err)
	}
	payload := recoveryCommands(emitted, first.RecoveryHandles)[1].handle + " 1:ef86\n"
	history, err := transform.translateRecovery("call-recovery", payload, nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("recovery = %+v, %v", history, err)
	}
	if !history.Applied || !history.confirmed || history.Patch != "" || strings.Contains(history.carrierInput(), "apply_patch") || strings.Contains(history.carrierInput(), "exec_command") {
		t.Fatalf("retained recovery used host patch carrier: %+v", history)
	}
	if history.ToolName != mekugiRecoveryToolName || history.Script != payload || history.CorrelationID != first.CorrelationID || history.Attempt != 2 || !strings.HasPrefix(history.Evaluated, "in "+reference+"\n") {
		t.Fatalf("recovery identity = %+v", history)
	}
	path := filepath.Join(transform.shellDirectory, "call-shell")
	if content, err := os.ReadFile(path); err != nil || string(content) != "printf fixed\n" {
		t.Fatalf("applied content = %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(transform.directory, "@shell")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private recovery touched workspace: %v", err)
	}
	if replay, err := transform.translateRecovery("call-recovery", payload, nil); err != nil || replay.Evaluated != history.Evaluated || !replay.Applied {
		t.Fatalf("recovery replay = %+v, %v", replay, err)
	}
}

func TestInterruptedTerminalWithoutStatusDoesNotApplyUnfinishedShellEdit(t *testing.T) {
	for _, status := range []string{"failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
			var output []any
			for _, id := range []string{"complete", "unfinished"} {
				reference, _, retained := proxy.retainShell(transform.shellDirectory, id, "printf ok\n")
				if !retained {
					t.Fatal("shell script was not retained")
				}
				item := testMekugiItem()
				item["id"], item["call_id"] = "item-"+id, "call-"+id
				item["input"] = "in " + reference + "\ntype 1:ef86 \"printf fixed\"\n"
				if id == "unfinished" {
					item["status"] = "incomplete"
				}
				output = append(output, item)
			}
			events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type":     "response." + status,
				"response": map[string]any{"output": output},
			}))
			if err != nil || len(events) != 1 {
				t.Fatalf("terminal = %s, %v", events, err)
			}
			for id, want := range map[string]string{"complete": "printf fixed\n", "unfinished": "printf ok\n"} {
				got, err := os.ReadFile(filepath.Join(transform.shellDirectory, id))
				if err != nil || string(got) != want {
					t.Fatalf("%s content = %q, %v", id, got, err)
				}
			}
			history, remembered := proxy.history(transform.historySessionID, "call-complete")
			if !remembered || !history.Applied || !history.confirmed {
				t.Fatalf("completed replay = %+v, %v", history, remembered)
			}
			if _, remembered := proxy.history(transform.historySessionID, "call-unfinished"); remembered {
				t.Fatal("unfinished private edit entered replay history")
			}
		})
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

func TestShellResultMetadata(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	shell, ok := proxy.registry.contribution("shell")
	if !ok {
		t.Fatal("shell contribution is unavailable")
	}
	carrier, err := proxy.registry.execCarrierPayload(
		codeModeCarrierCustom,
		shell,
		"printf ok",
		[]string{"bash", "printf ok"},
		"",
		nil,
		map[string]json.RawMessage{"retained": mustMarshalJSON(false)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(carrier, `"retained":false`) {
		t.Fatalf("carrier omitted retention result: %s", carrier)
	}
}
