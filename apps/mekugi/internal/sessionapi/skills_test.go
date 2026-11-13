package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

type skillCatalogRuntime struct {
	NativeRuntime
	calls atomic.Int32
}

func (r *skillCatalogRuntime) NativeSkillCatalog(_ context.Context, thread string) (map[string][]string, error) {
	r.calls.Add(1)
	if thread != "child" {
		return nil, errors.New("foreign runtime identity")
	}
	return map[string][]string{"native": {"/native/SKILL.md"}}, errors.New("partial catalog failure")
}

func TestNativeSkillCatalogRPCAdmissionAndWorkerSocket(t *testing.T) {
	hub := NewHub(t.Context(), nil)
	server := NewLive()
	runtime := new(skillCatalogRuntime)
	server.BindRequests(runtime)
	registerTestSession(hub, "child", server, Summary{Thread: "child"})
	hub.sessions["child"].runtime = &managedSession{ctx: t.Context(), server: server}
	for _, native := range []bool{false, true} {
		conn := rawControl(t, hub.Handler(), native)
		controlInitialize(t, conn)
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"skills","method":"source/skills","params":{"thread":"child"}}`)
		reply := controlRead(t, conn)
		if !native {
			if reply.Error == nil || reply.Error.Data.Status != 403 || runtime.calls.Load() != 0 {
				t.Fatalf("remote caller acquired native metadata: %+v", reply)
			}
			continue
		}
		var catalog nativeSkillCatalog
		if reply.Error != nil || json.Unmarshal(reply.Result, &catalog) != nil || catalog.Problem != "partial catalog failure" || len(catalog.Paths["native"]) != 1 {
			t.Fatalf("native caller lost partial catalog: %+v %+v", reply, catalog)
		}
		controlWrite(t, conn, `{"jsonrpc":"2.0","id":"foreign","method":"source/skills","params":{"thread":"unknown"}}`)
		if reply := controlRead(t, conn); reply.Error == nil || reply.Error.Data.Status != 409 || runtime.calls.Load() != 1 {
			t.Fatalf("unknown thread reached a runtime: %+v", reply)
		}
	}
	// Exercise the actual worker client on a nondefault private Unix socket.
	path := filepath.Join(t.TempDir(), "control.sock")
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: Versioned(hub.Handler())}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	t.Setenv(NativeSkillsSocketEnvironment, path)
	paths, err := ReadNativeSkillCatalog(t.Context(), "child")
	if !reflect.DeepEqual(paths, map[string][]string{"native": {"/native/SKILL.md"}}) || err == nil || err.Error() != "partial catalog failure" {
		t.Fatalf("worker catalog transport: %+v %v", paths, err)
	}
	if _, err := ReadNativeSkillCatalog(t.Context(), ""); err == nil {
		t.Fatal("worker accepted an unselected thread")
	}
	server.BindRequests(struct{ NativeRuntime }{})
	paths, err = ReadNativeSkillCatalog(t.Context(), "child")
	if err != nil || len(paths) != 0 {
		t.Fatalf("backend without native skills lost managed fallback: %+v %v", paths, err)
	}
}
