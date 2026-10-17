package router

func testAppServerIntegration(p *mekugiProxy) appServerIntegration {
	if p == nil {
		return appServerIntegration{}
	}
	integration := appServerIntegration{controlObservations: p.control, usage: p.usage, audience: p.activity, review: p.review, execWindows: p.execWindows, nativeTrace: p.nativeTrace, retained: p.replayStore, captureNotice: p.noticeSink}
	if p.execWindows != nil {
		integration.execTrack = p.execWindows.tracker
	}
	return integration
}
func setTestAppServerIntegration(u *appServerUI, p *mekugiProxy) {
	integration := testAppServerIntegration(p)
	u.controlObservations = integration.controlObservations
	u.usage = integration.usage
	u.audience = integration.audience
	u.review = integration.review
	u.execWindows = integration.execWindows
	u.nativeTrace = integration.nativeTrace
	u.retained = integration.retained
	u.captureNotice = integration.captureNotice
}
