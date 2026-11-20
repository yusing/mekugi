//go:build !linux && !darwin

package query

func prepareFrontendCleanup() error { return nil }

func cleanupFrontendOrphans() {}
