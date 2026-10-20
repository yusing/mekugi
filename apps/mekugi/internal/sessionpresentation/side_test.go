package sessionpresentation_test

import (
	"errors"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"testing"
)

func TestSideCloseTargetsPublishedExchange(t *testing.T) {
	c := newStatusConsumer()
	c.conversation.Side = new(conversationmodel.SideAnswer)
	c.conversation.Side.Begin("same question", conversationmodel.Input{})
	v := c.publish(sessionpresentation.Caption{})
	action := sessionpresentation.CloseSideIntent{Version: v.Version, Exchange: v.Side.ID}
	if err := c.model().AdmitCloseSide(action); err != nil {
		t.Fatal(err)
	}
	c.conversation.Side.Begin("same question", conversationmodel.Input{})
	next := c.publish(sessionpresentation.Caption{})
	if next.Side.ID == v.Side.ID {
		t.Fatal("repeated question reused exchange identity")
	}
	if !errors.Is(c.model().AdmitCloseSide(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("old close dismissed a follow-up")
	}
	action.Version = next.Version
	if !errors.Is(c.model().AdmitCloseSide(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("wrong exchange admitted")
	}
}
