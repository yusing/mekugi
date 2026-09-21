//go:build !unix

package router

func processHasControllingTerminal() bool { return false }
