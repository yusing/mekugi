package commandsegments_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/commandsegments"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestDescriptorAcquiresTransportOnlyDuringStartAndRemovesItOnStop(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	owner := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, nil)
	service, err := commandsegments.Module.New(commandsegments.Deps{History: owner, Live: true}, module.Slots{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := commandsegments.Module.Stop(context.WithoutCancel(t.Context()), service); err != nil {
			t.Error(err)
		}
	})
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || service.Live != nil {
		t.Fatalf("construction acquired resources: entries=%v live=%v err=%v", entries, service.Live, err)
	}
	if err := commandsegments.Module.Start(t.Context(), service); err != nil {
		t.Fatal(err)
	}
	if service.Live == nil || service.LiveError != nil {
		t.Fatalf("enabled startup: live=%v err=%v", service.Live, service.LiveError)
	}
	endpoints := service.Live.Endpoints()
	info, err := os.Stat(endpoints.Requests)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("requests endpoint is not a FIFO: info=%v err=%v", info, err)
	}
	if filepath.Dir(endpoints.Requests) != filepath.Dir(endpoints.Directory) {
		t.Fatalf("endpoints have different invocation roots: %+v", endpoints)
	}
	if err := commandsegments.Module.Stop(t.Context(), service); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(endpoints.Requests)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stop left private resources: %v", err)
	}
}

func TestDescriptorReturnsStartupCancellation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	owner := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, nil)
	service, err := commandsegments.Module.New(commandsegments.Deps{History: owner, Live: true}, module.Slots{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := commandsegments.Module.Start(ctx, service); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup cancellation became optional failure: %v", err)
	}
	if service.Live != nil || !errors.Is(service.LiveError, context.Canceled) {
		t.Fatalf("cancelled startup: live=%v err=%v", service.Live, service.LiveError)
	}
	if err := commandsegments.Module.Stop(t.Context(), service); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled startup acquired resources: entries=%v err=%v", entries, err)
	}
}
