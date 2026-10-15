package router

import (
	"context"
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
)

func (s *mekugiReplayStore) putChangeRead(ctx context.Context, snapshot *changeReadSnapshot, text string, offset int) (string, error) {
	snapshot.Offset, snapshot.Digest, snapshot.Frozen = offset, fmt.Sprintf("%x", sha256.Sum256([]byte(text))), true
	data, err := json.Marshal(snapshot, jsonv1.DefaultOptionsV1())
	if err != nil {
		return "", err
	}
	session, err := s.recoverySession(ctx)
	if err != nil {
		return "", err
	}
	handles, err := session.Reserve(ctx, 1)
	if err != nil {
		return "", err
	}
	id, err := session.PublishSelectionAt(ctx, handles[0], data)
	return id, storagePublicationError(err)
}
