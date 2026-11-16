//go:build !windows

package main

import "os/exec"

type launchMode int

const (
	launchModeKiosk launchMode = iota
	launchModeApp
	launchModeFallback
)

func initDpiAwareness() {}

func acquireSingleInstance() bool { return true }

func keepSystemAwake(stop <-chan struct{}) {
	<-stop
}

func cleanupStaleProfileDirs() {}

func prepareProfileDir(profileDir string) {}

func hasVisibleKioskWindow() bool { return false }

func closeAppWindows(kmd *exec.Cmd, profileDir string) {
	if kmd != nil && kmd.Process != nil {
		_ = kmd.Process.Kill()
	}
}

func launchKioskBrowser(mode launchMode, url string, profileDir string) *exec.Cmd {
	return nil
}

