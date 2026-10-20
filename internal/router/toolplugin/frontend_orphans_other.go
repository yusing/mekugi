//go:build !linux

package toolplugin

func enableFrontendSubreaper() error { return nil }

func cleanupFrontendOrphans() {}
