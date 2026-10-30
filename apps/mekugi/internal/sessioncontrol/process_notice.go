package sessioncontrol

import "fmt"

func processCancellationNotice(processID string) string {
	return fmt.Sprintf("The user canceled exec session %s. Do not restart the canceled operation without a new user request.", processID)
}

func processTerminationFailureNotice(processID, detail string) string {
	return fmt.Sprintf("Termination of canceled exec session %s was not confirmed: %s. Its native process or descendants may still be running.", processID, detail)
}
