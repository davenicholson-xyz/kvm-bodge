//go:build linux

package main

import (
	"os/exec"
	"strings"
)

func writeClipboard(text string) {
	if path, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.Command(path)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return
		}
	}
	if path, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command(path, "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			dbg("xclip: %v", err)
		}
		return
	}
	dbg("writeClipboard: neither wl-copy nor xclip found")
}
