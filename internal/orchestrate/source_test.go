package orchestrate

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareExcludedVCS(t *testing.T) {
	for _, marker := range []string{".hg", ".jj", ".bzr"} {
		t.Run(marker, func(t *testing.T) {
			s, source, _ := launchFixture(t)
			if err := os.Mkdir(filepath.Join(source, marker), 0700); err != nil {
				t.Fatal(err)
			}
			s.ShadowSnapshot = func(context.Context, string, string) (string, error) {
				t.Fatal("versioned source was treated as unversioned")
				return "", errors.New("unexpected snapshot")
			}
			_, err := s.Prepare(t.Context(), source, "main", "excluded")
			want := "supports Git, SVN and unversioned shadow only"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal("unexpected preparation result", err)
			}
			batches, err := s.List(t.Context(), source, "main")
			if err != nil || len(batches) != 1 || batches[0].TaskName != "batch" {
				t.Fatal("rejection changed existing run", batches, err)
			}
		})
	}
}

func TestRemovedAdapterRetainsWork(t *testing.T) {
	s, source, b := launchFixture(t)
	file := filepath.Join(b.Checkout, "unfinished")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.withRun(t.Context(), source, "main", func(m *manifest, path string) error {
		m.Batches[0].VCS = "hg"
		return s.save(m, path)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(t.Context(), source, "main", "batch"); err == nil {
		t.Fatal("removed adapter prepared again")
	}
	if _, dispatch, err := s.BeginLaunch(t.Context(), source, "main", "batch", "work", jsontext.Value(`{}`)); err == nil || dispatch {
		t.Fatal("removed adapter authorized launch", err)
	}
	if err := s.withRun(t.Context(), source, "main", func(m *manifest, path string) error {
		m.Batches[0].State = "launched"
		m.Batches[0].Launch = &Launch{ThreadID: "child"}
		return s.save(m, path)
	}); err != nil {
		t.Fatal(err)
	}
	if _, dispatch, err := s.BeginDelivery(t.Context(), source, "main", Delivery{ID: "new", From: "main", Target: "child", Message: "work"}); err == nil || dispatch {
		t.Fatal("removed adapter authorized follow-up", err)
	}
	if _, err := s.RecordIntegration(t.Context(), source, "main", "batch", "child"); err == nil {
		t.Fatal("removed adapter accepted integration")
	}
	batches, err := (&Store{Directory: s.Directory}).List(t.Context(), source, "main")
	if err != nil || len(batches) != 1 || batches[0].VCS != "hg" {
		t.Fatal("removed adapter lost retained identity", batches, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
		t.Fatal("removed adapter lost unfinished files", err)
	}
}
