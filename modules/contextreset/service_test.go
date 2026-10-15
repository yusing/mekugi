package contextreset_test

import (
	"context"
	jsonv1 "encoding/json"
	"errors"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/contextreset"
	"github.com/yusing/mekugi/modules/contextreset/codexbinding"
	"github.com/yusing/mekugi/modules/contextreset/guidance"
	"github.com/yusing/mekugi/modules/fileread"
	"github.com/yusing/mekugi/modules/sourceoutline"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func collectResetGuidance(t *testing.T, items []map[string]jsonv1.RawMessage, base, workspace string, firstOrder []string, retained map[string][]guidance.Skill) *guidance.Guidance {
	t.Helper()
	script := &moduletest.Scripted{}
	service := moduletest.New(t, contextreset.Module, contextreset.Deps{
		Reader:  moduletest.New(t, fileread.Module, fileread.Deps{}, module.Slots{}, script),
		Outline: moduletest.New(t, sourceoutline.Module, sourceoutline.Deps{}, module.Slots{}, script),
		ReadSkill: func(context.Context, string, string, string) ([]byte, bool, error) {
			return nil, false, errors.New("no skill source supplied")
		},
	}, module.Slots{}, script)
	return service.Collect(t.Context(), codexbinding.Decode(items, base, workspace, firstOrder, retained))
}
