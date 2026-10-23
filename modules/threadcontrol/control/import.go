package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func (s *Service) ImportPath(thread, name string) (string, error) {
	owner, err := s.owner(thread)
	if err != nil {
		return "", err
	}
	if filepath.Base(name) != name || !strings.HasPrefix(name, "threadcontrol-import-") {
		return "", errors.New("invalid retained fork import")
	}
	return filepath.Join(owner.session.Store.Directory(), name), nil
}

func (s *Service) PublishImport(ctx context.Context, caller Identity, data []byte) (string, error) {
	if _, err := s.Caller(caller); err != nil {
		return "", err
	}
	owner, err := s.owner(caller.Thread)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("threadcontrol-import-%x", sha256.Sum256([]byte(caller.Thread+"\x00"+caller.Call)))
	err = owner.session.Store.Transaction(ctx, owner.session.Writes, func(tx *history.Transaction) error {
		prior, err := history.ReadRecord(filepath.Join(owner.session.Store.Directory(), name))
		if err == nil && !bytes.Equal(prior, data) {
			return errors.New("conflicting retained fork preparation")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := tx.Admit(owner.session.Limits, name, int64(len(data))); err != nil {
			return err
		}
		if err := tx.Write(name, "threadcontrol-import-pending-", data); err != nil {
			return err
		}
		return tx.Retain(caller.Thread, owner.session.Limits, name)
	})
	return name, err
}
