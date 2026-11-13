//go:build !windows

package main

import "os/exec"

func acquireSingleInstance() bool { return true }

func keepSystemAwake(stop <-chan struct{}) {
	<-stop
}

func launchKioskBrowser(url string, profileDir string) *exec.Cmd {
	return nil
}

func launchHdmiWindow(url string, profileDir string) {}
