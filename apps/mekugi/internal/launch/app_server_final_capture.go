package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"sync"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

type finalCaptureReply struct {
	id        jsontext.Value
	capture   *appserver.FrozenFinalCapture
	admission *appserver.ChildHostAdmissionResponse
	result    any
	err       error
}

// The invocation owns acquisition; only its event loop writes to the connection.
type nativeFinalCaptures struct {
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
	results chan finalCaptureReply
	pending map[string]context.CancelFunc // Exact native callback IDs, event-loop owned.
	prepare func(context.Context, appserver.FinalCaptureRequest) (*appserver.FrozenFinalCapture, error)
	admit   func(context.Context, appserver.ChildHostAdmissionRequest) error
	freeze  func(context.Context, appserver.ProgressFreezeRequest) (appserver.ProgressFreezeResponse, error)
	output  func(context.Context, appserver.ProgressOutputRequest) (appserver.ProgressOutputResponse, error)
	project func(context.Context, appserver.FinalCaptureProjectRequest) (appserver.FinalCaptureProjectResponse, error)
	foreign func(context.Context, appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error)
}

func (c *nativeFinalCaptures) close() {
	c.cancel()
	c.workers.Wait()
}

func (c *nativeFinalCaptures) begin(id jsontext.Value) (context.Context, bool) {
	if c.pending == nil {
		c.pending = make(map[string]context.CancelFunc)
	}
	if c.pending[string(id)] != nil {
		return nil, false
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.pending[string(id)] = cancel
	return ctx, true
}

func (session *nativeSession) finalCaptureMessage(m appserver.Message) (bool, error) {
	if m.Method == "serverRequest/resolved" && session.finalCaptures != nil {
		var resolved struct {
			RequestID jsontext.Value `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &resolved) == nil {
			owner := session.finalCaptures
			if cancel := owner.pending[string(resolved.RequestID)]; cancel != nil {
				delete(owner.pending, string(resolved.RequestID))
				cancel()
				return true, nil
			}
		}
	}
	if len(m.ID) != 0 {
		switch m.Method {
		case "agent/progress/freeze", "agent/progress/prepareOutput", "agent/finalCapture/project", "agent/foreign/route":
			owner := session.finalCaptures
			if owner == nil {
				return true, session.Client.Host.RespondError(m.ID, appserver.Error{Code: -32603, Message: "native capture resources are unavailable"})
			}
			switch m.Method {
			case "agent/foreign/route":
				return true, nativeCaptureCallback(session, m, owner.foreign)
			case "agent/progress/freeze":
				return true, nativeCaptureCallback(session, m, owner.freeze)
			case "agent/progress/prepareOutput":
				return true, nativeCaptureCallback(session, m, owner.output)
			default:
				return true, nativeCaptureCallback(session, m, owner.project)
			}
		}
	}
	if m.Method == "agent/hostAdmission/prepare" && len(m.ID) != 0 {
		var input appserver.ChildHostAdmissionRequest
		if err := json.Unmarshal(m.Params, &input); err != nil {
			return true, session.Client.Host.RespondError(m.ID, appserver.Error{Code: -32602, Message: "invalid native child admission: " + err.Error()})
		}
		owner := session.finalCaptures
		if owner == nil || owner.admit == nil {
			return true, session.Client.Host.RespondError(m.ID, appserver.Error{Code: -32603, Message: "native child admission owner is unavailable"})
		}
		ctx, started := owner.begin(m.ID)
		if !started {
			return true, nil
		}
		owner.workers.Go(func() {
			err := owner.admit(ctx, input)
			select {
			case owner.results <- finalCaptureReply{id: m.ID, admission: &appserver.ChildHostAdmissionResponse{ThreadID: input.ThreadID}, err: err}:
			case <-ctx.Done():
			}
		})
		return true, nil
	}
	if m.Method != "agent/finalCapture/prepare" || len(m.ID) == 0 {
		return false, nil
	}
	var input appserver.FinalCaptureRequest
	if err := json.Unmarshal(m.Params, &input); err != nil {
		return true, session.Client.Host.RespondError(m.ID, appserver.Error{Code: -32602, Message: "invalid final capture request: " + err.Error()})
	}
	owner := session.finalCaptures
	if owner == nil || owner.prepare == nil {
		return true, session.Client.Host.RespondError(m.ID, appserver.Error{Code: -32603, Message: "final capture owner is unavailable"})
	}
	ctx, started := owner.begin(m.ID)
	if !started {
		return true, nil
	}
	owner.workers.Go(func() {
		capture, err := owner.prepare(ctx, input)
		select {
		case owner.results <- finalCaptureReply{id: m.ID, capture: capture, err: err}:
		case <-ctx.Done():
		}
	})
	return true, nil
}

func nativeCaptureCallback[Input, Output any](session *nativeSession, m appserver.Message, acquire func(context.Context, Input) (Output, error)) error {
	if acquire == nil {
		return session.Client.Host.RespondError(m.ID, appserver.Error{Code: -32603, Message: "native capture resource owner is unavailable"})
	}
	var input Input
	if err := json.Unmarshal(m.Params, &input); err != nil {
		return session.Client.Host.RespondError(m.ID, appserver.Error{Code: -32602, Message: "invalid native capture request: " + err.Error()})
	}
	owner := session.finalCaptures
	ctx, started := owner.begin(m.ID)
	if !started {
		return nil
	}
	owner.workers.Go(func() {
		result, err := acquire(ctx, input)
		select {
		case owner.results <- finalCaptureReply{id: m.ID, result: result, err: err}:
		case <-ctx.Done():
		}
	})
	return nil
}

func (session *nativeSession) finalCaptureReply(reply finalCaptureReply) error {
	owner := session.finalCaptures
	cancel := owner.pending[string(reply.id)]
	if cancel == nil {
		return nil
	} // Native canceled this exact callback before delivery.
	delete(owner.pending, string(reply.id))
	cancel()
	if reply.err != nil {
		if errors.Is(reply.err, context.Canceled) && session.finalCaptures.ctx.Err() != nil {
			return nil
		}
		return session.Client.Host.RespondError(reply.id, appserver.Error{Code: -32603, Message: reply.err.Error()})
	}
	if reply.admission != nil {
		return session.Client.Requests.Respond(reply.id, reply.admission)
	}
	if reply.result != nil {
		return session.Client.Requests.Respond(reply.id, reply.result)
	}
	return session.Client.Requests.Respond(reply.id, &appserver.FinalCaptureResponse{Capture: reply.capture})
}
