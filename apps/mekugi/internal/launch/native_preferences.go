package launch

import (
	"encoding/json/jsontext"
	"errors"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// nativePreferenceWriter serializes publication through the actual preference
// owners. While storage is busy, new state stays staged on those attachments;
// there is no queue of copied writes. Replacement retains only outgoing owners
// that still need their final preparation, preserving per-path write order.
type nativePreferenceWriter struct {
	active      <-chan error
	activeOwner *preferences.Attachment
	outgoing    []*preferences.Attachment
}

func (w *nativePreferenceWriter) step(current *preferences.Attachment, now time.Time, flush bool) (failure error) {
	if w.active != nil {
		select {
		case failure = <-w.active:
			w.active, w.activeOwner = nil, nil
		default:
			return nil
		}
	}
	for {
		owner, final := current, flush
		if len(w.outgoing) > 0 {
			owner, final = w.outgoing[0], true
		}
		if owner == nil {
			return failure
		}
		write, err := owner.PrepareSave(now, final)
		failure = errors.Join(failure, err)
		if len(w.outgoing) > 0 {
			w.outgoing[0] = nil
			w.outgoing = w.outgoing[1:]
		} else if write == nil {
			return failure
		}
		if write == nil {
			continue
		}
		done := make(chan error, 1)
		w.active, w.activeOwner = done, owner
		go func() { done <- write.Publish() }()
		return failure
	}
}

func (w *nativePreferenceWriter) close(current *preferences.Attachment, now time.Time) (failure error) {
	for {
		failure = errors.Join(failure, w.step(current, now, true))
		if w.active == nil {
			return failure
		}
		failure = errors.Join(failure, <-w.active)
		w.active, w.activeOwner = nil, nil
	}
}

func (p *nativePreferences) savePrepared(now time.Time, flush bool) error {
	if p == nil {
		return nil
	}
	return p.writer.step(p.attachment, now, flush)
}

func (p *nativePreferences) stageNative(data jsontext.Value, now time.Time, flush bool) error {
	if p == nil || p.attachment == nil {
		return errors.New("Wait for the session preferences to be ready.")
	}
	if _, err := terminalsession.ReadPreferences(data); err != nil {
		return err
	}
	p.attachment.StagePresentation(data)
	return p.savePrepared(now, flush)
}
