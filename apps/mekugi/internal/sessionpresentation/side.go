package sessionpresentation

type CloseSideIntent struct {
	Version  Version
	Exchange uint64
}

func (m *Model) AdmitCloseSide(in CloseSideIntent) error {
	if in.Version != m.view.Version || m.view.Side == nil || in.Exchange != m.view.Side.ID {
		return ErrStaleIntent
	}
	if m.replay {
		return ErrReplayIntent
	}
	return nil
}
