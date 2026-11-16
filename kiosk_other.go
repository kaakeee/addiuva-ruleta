//go:build !windows

package main

import "os/exec"

type HdmiDisplayInfo struct {
	Monitors     int   `json:"monitors"`
	HasSecondary bool  `json:"hasSecondary"`
	Width        int32 `json:"width"`
	Height       int32 `json:"height"`
}

func initDpiAwareness() {}

func acquireSingleInstance() bool { return true }

func getHdmiDisplayInfo() HdmiDisplayInfo {
	return HdmiDisplayInfo{Monitors: 1, HasSecondary: false, Width: 1920, Height: 1080}
}

func keepSystemAwake(stop <-chan struct{}) {
	<-stop
}

func cleanupStaleProfileDirs() {}

func closeAppWindows(kmd *exec.Cmd, hdmiCmd *exec.Cmd, profileDir string) {
	if hdmiCmd != nil && hdmiCmd.Process != nil {
		_ = hdmiCmd.Process.Kill()
	}
	if kmd != nil && kmd.Process != nil {
		_ = kmd.Process.Kill()
	}
}

func launchKioskBrowser(url string, profileDir string) *exec.Cmd {
	return nil
}

func launchHdmiWindow(url string, profileDir string) (HdmiDisplayInfo, *exec.Cmd) {
	return getHdmiDisplayInfo(), nil
}
