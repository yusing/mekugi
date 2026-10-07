package router

// Submission to the SDK is not proof that native execution consumed a choice.
// Matching native execution or result evidence confirms it without interpreting
// tool failure text as a permission decision.
func (u *appServerUI) confirmRuntimePermission(id string) {
	outcome := u.runtime.permissionChoices[id]
	if id == "" || outcome == "" {
		return
	}
	delete(u.runtime.permissionChoices, id)
	if owner := u.approvalItem(u.thread, "", id); owner != nil && (owner.approval == "Cancelled" || owner.approval == "Auto Denied") {
		return
	}
	u.recordApproval(&nativeApproval{thread: u.thread, item: id}, outcome)
}
