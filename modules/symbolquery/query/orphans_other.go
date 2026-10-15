//go:build !linux

package query

func enableFrontendSubreaper() error { return nil }

func cleanupFrontendOrphans() {}
