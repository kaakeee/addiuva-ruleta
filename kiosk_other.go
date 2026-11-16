//go:build !windows

package main

import "os/exec"

func initDpiAwareness() {}

func acquireSingleInstance() bool { return true }

func keepSystemAwake(stop <-chan struct{}) {
	<-stop
}

func cleanupStaleProfileDirs() {}

func closeAppWindows(kmd *exec.Cmd, profileDir string) {
	if kmd != nil && kmd.Process != nil {
		_ = kmd.Process.Kill()
	}
}

func launchKioskBrowser(url string, profileDir string) *exec.Cmd {
	return nil
}
