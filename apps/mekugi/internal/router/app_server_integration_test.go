package router

func testAppServerIntegration(p *observationFixture) appServerIntegration {
	if p == nil {
		return appServerIntegration{}
	}
	integration := appServerIntegration{controlObservations: p.control, usage: p.usage, audience: p.activity, review: p.review, execWindows: p.execWindows, nativeTrace: p.nativeTrace, retained: p.retained, changeSource: p.changeSource, snapshots: p.snapshots, recovered: p.recovered, publishChanges: p.publishChanges, captureNotice: p.noticeSink}
	if p.execWindows != nil {
		integration.execTrack = p.execWindows.Reports
	}
	return integration
}
func setTestAppServerIntegration(u *appServerUI, p *observationFixture) {
	integration := testAppServerIntegration(p)
	u.controlObservations = integration.controlObservations
	u.usage = integration.usage
	u.audience = integration.audience
	u.review = integration.review
	u.execWindows = integration.execWindows
	u.nativeTrace = integration.nativeTrace
	u.retained = integration.retained
	u.changeSource = integration.changeSource
	u.snapshots = integration.snapshots
	u.recovered = integration.recovered
	u.publishChanges = integration.publishChanges
	u.captureNotice = integration.captureNotice
}
