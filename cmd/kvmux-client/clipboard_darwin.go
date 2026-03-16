//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

func writeClipboard(text string) {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		dbg("pbcopy: %v", err)
	}
}
