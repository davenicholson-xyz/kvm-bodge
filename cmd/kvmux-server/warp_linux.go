//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// uinput ioctl codes (x86-64 Linux).
const (
	uiSetEvBit  uintptr = 0x40045564
	uiSetKeyBit uintptr = 0x40045565
	uiSetRelBit uintptr = 0x40045566
	uiDevCreate uintptr = 0x5501
	uiDevDestroy uintptr = 0x5502

	evSyn     uint16 = 0x00
	evKey     uint16 = 0x01
	evRel     uint16 = 0x02
	relX      uint16 = 0x00
	relY      uint16 = 0x01
	synReport uint16 = 0x00
	btnLeft   uint16 = 0x110
	busUSB    uint16 = 0x03

	uinputNameLen = 80
	absCnt        = 64
)

// uinputUserDev is the legacy uinput device descriptor (1116 bytes).
type uiInputID struct{ Bustype, Vendor, Product, Version uint16 }
type uinputUserDev struct {
	Name         [uinputNameLen]byte
	ID           uiInputID
	FfEffectsMax uint32
	Absmax       [absCnt]int32
	Absmin       [absCnt]int32
	Absfuzz      [absCnt]int32
	Absflat      [absCnt]int32
}

// warpEvent mirrors struct input_event on 64-bit Linux (24 bytes).
type warpEvent struct {
	Sec, Usec  int64
	Type, Code uint16
	Value      int32
}

// warpMouseToCenter attempts to move the OS cursor to (w/2, h/2) and returns
// the virtual starting position (vx, vy) that matches where the cursor ended up.
// If only a corner slam is possible it returns (0, 0).
func warpMouseToCenter(w, h int) (vx, vy int) {
	// --- try Hyprland IPC first (logical coordinates, no scale confusion) ---
	if err := warpCursorHyprland(w/2, h/2); err == nil {
		log.Printf("cursor warped to centre (%d,%d) via hyprctl", w/2, h/2)
		return w / 2, h / 2
	}

	// --- try xdotool ---
	display, xauth := findDisplayEnv()
	xdotool := findBin("xdotool")
	if xdotool != "" && display != "" {
		env := []string{"DISPLAY=" + display}
		if xauth != "" {
			env = append(env, "XAUTHORITY="+xauth)
		}
		cmd := exec.Command(xdotool, "mousemove", strconv.Itoa(w/2), strconv.Itoa(h/2))
		cmd.Env = env
		if err := cmd.Run(); err != nil {
			log.Printf("xdotool mousemove: %v", err)
		} else {
			log.Printf("cursor warped to centre (%d,%d) via xdotool", w/2, h/2)
			return w / 2, h / 2
		}
	} else {
		log.Printf("xdotool unavailable (binary=%q display=%q)", xdotool, display)
	}

	// --- fall back to uinput slam-to-corner ---
	if err := slamToCorner(); err != nil {
		log.Printf("uinput corner slam: %v — move cursor to centre manually", err)
		return w / 2, h / 2 // unknown position; guess centre
	}
	log.Printf("cursor slammed to top-left corner via uinput; tracking from (0,0)")
	return 0, 0
}

// findDisplayEnv scans /proc to find DISPLAY and XAUTHORITY from any process
// owned by the real user (works when running under sudo).
func findDisplayEnv() (display, xauth string) {
	// env vars set in current process first
	display = os.Getenv("DISPLAY")
	xauth = os.Getenv("XAUTHORITY")
	if display != "" {
		return
	}

	// find the real user's UID
	username := os.Getenv("SUDO_USER")
	if username == "" {
		username = os.Getenv("USER")
	}
	u, err := user.Lookup(username)
	if err != nil {
		return
	}

	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := os.Lstat("/proc/" + e.Name())
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || strconv.Itoa(int(st.Uid)) != u.Uid {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue
		}
		for _, kv := range strings.Split(string(data), "\x00") {
			if display == "" && strings.HasPrefix(kv, "DISPLAY=") {
				display = strings.TrimPrefix(kv, "DISPLAY=")
			}
			if xauth == "" && strings.HasPrefix(kv, "XAUTHORITY=") {
				xauth = strings.TrimPrefix(kv, "XAUTHORITY=")
			}
		}
		if display != "" && xauth != "" {
			break
		}
	}
	return
}

// findHyprlandEnv finds HYPRLAND_INSTANCE_SIGNATURE and XDG_RUNTIME_DIR from
// the user's process environment (needed when running under sudo).
func findHyprlandEnv() (sig, runtimeDir string) {
	sig = os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	runtimeDir = os.Getenv("XDG_RUNTIME_DIR")
	if sig != "" && runtimeDir != "" {
		return
	}
	username := os.Getenv("SUDO_USER")
	if username == "" {
		username = os.Getenv("USER")
	}
	u, err := user.Lookup(username)
	if err != nil {
		return
	}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := os.Lstat("/proc/" + e.Name())
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || strconv.Itoa(int(st.Uid)) != u.Uid {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue
		}
		for _, kv := range strings.Split(string(data), "\x00") {
			if sig == "" && strings.HasPrefix(kv, "HYPRLAND_INSTANCE_SIGNATURE=") {
				sig = strings.TrimPrefix(kv, "HYPRLAND_INSTANCE_SIGNATURE=")
			}
			if runtimeDir == "" && strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") {
				runtimeDir = strings.TrimPrefix(kv, "XDG_RUNTIME_DIR=")
			}
		}
		if sig != "" && runtimeDir != "" {
			break
		}
	}
	return
}

// runHyprctl runs hyprctl with the Hyprland socket env set correctly.
func runHyprctl(args ...string) (string, error) {
	hyprctl := findBin("hyprctl")
	if hyprctl == "" {
		return "", fmt.Errorf("hyprctl not found")
	}
	sig, runtimeDir := findHyprlandEnv()
	if sig == "" {
		return "", fmt.Errorf("HYPRLAND_INSTANCE_SIGNATURE not found")
	}
	env := []string{
		"HYPRLAND_INSTANCE_SIGNATURE=" + sig,
	}
	if runtimeDir != "" {
		env = append(env, "XDG_RUNTIME_DIR="+runtimeDir)
	}
	cmd := exec.Command(hyprctl, args...)
	cmd.Env = env
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// hyprSocket sends a single command directly to the Hyprland IPC socket and
// returns the response. Much faster than spawning hyprctl as a subprocess.
func hyprSocket(cmd string) (string, error) {
	sig, runtimeDir := findHyprlandEnv()
	if sig == "" || runtimeDir == "" {
		return "", fmt.Errorf("hyprland env not found")
	}
	conn, err := net.DialTimeout("unix", runtimeDir+"/hypr/"+sig+"/.socket.sock", 100*time.Millisecond)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(100 * time.Millisecond)) //nolint:errcheck
	conn.Write([]byte(cmd))                                  //nolint:errcheck
	out, err := io.ReadAll(conn)
	return strings.TrimSpace(string(out)), err
}

// readCursorPosHyprland returns the cursor position in Wayland logical
// coordinates — the same space as our screenW/H.
func readCursorPosHyprland(w, h int) (int, int, bool) {
	out, err := hyprSocket("cursorpos")
	if err != nil {
		return 0, 0, false
	}
	var x, y int
	if n, _ := fmt.Sscanf(out, "%d, %d", &x, &y); n != 2 {
		if n, _ := fmt.Sscanf(out, "%d %d", &x, &y); n != 2 {
			return 0, 0, false
		}
	}
	return clamp(x, 0, w-1), clamp(y, 0, h-1), true
}

type hyprMonitor struct {
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Scale   float64 `json:"scale"`
	Focused bool    `json:"focused"`
}

// detectScreenSizeHyprland returns the logical screen dimensions and scale of
// the focused monitor via hyprctl monitors.
func detectScreenSizeHyprland() (w, h int, scale float64, err error) {
	out, err := runHyprctl("monitors", "-j")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("hyprctl monitors: %w", err)
	}
	var monitors []hyprMonitor
	if err := json.Unmarshal([]byte(out), &monitors); err != nil {
		return 0, 0, 0, fmt.Errorf("parse monitors JSON: %w", err)
	}
	if len(monitors) == 0 {
		return 0, 0, 0, fmt.Errorf("no monitors found")
	}
	m := monitors[0]
	for _, mon := range monitors {
		if mon.Focused {
			m = mon
			break
		}
	}
	scale = m.Scale
	if scale <= 0 {
		scale = 1.0
	}
	return int(math.Round(float64(m.Width) / scale)),
		int(math.Round(float64(m.Height) / scale)), scale, nil
}

// warpCursorHyprland moves the cursor to (x, y) in logical Wayland coordinates.
func warpCursorHyprland(x, y int) error {
	_, err := runHyprctl("dispatch", "movecursor", strconv.Itoa(x), strconv.Itoa(y))
	return err
}

// detectScreenByCornerSlam determines the screen dimensions in xdotool's
// coordinate space by moving the cursor to a far corner and reading where it
// actually lands. This is the most reliable method because it bypasses all
// scale-factor ambiguity — the returned size is guaranteed to match
// getmouselocation's coordinate space. The cursor is restored afterwards.
func detectScreenByCornerSlam() (w, h int, err error) {
	xdotool := findBin("xdotool")
	if xdotool == "" {
		return 0, 0, fmt.Errorf("xdotool not found")
	}
	display, xauth := findDisplayEnv()
	if display == "" {
		return 0, 0, fmt.Errorf("no DISPLAY found")
	}
	env := []string{"DISPLAY=" + display}
	if xauth != "" {
		env = append(env, "XAUTHORITY="+xauth)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(xdotool, args...)
		cmd.Env = env
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}

	// Remember where the cursor is so we can restore it.
	origOut, origErr := run("getmouselocation")

	// Slam to the far corner — the OS clamps it to the screen edge.
	if _, err := run("mousemove", "99999", "99999"); err != nil {
		return 0, 0, fmt.Errorf("xdotool mousemove: %w", err)
	}
	out, err := run("getmouselocation")
	if err != nil {
		return 0, 0, fmt.Errorf("xdotool getmouselocation: %w", err)
	}
	var maxX, maxY int
	if n, _ := fmt.Sscanf(out, "x:%d y:%d", &maxX, &maxY); n != 2 || maxX <= 0 || maxY <= 0 {
		return 0, 0, fmt.Errorf("unexpected getmouselocation output: %q", out)
	}

	// Restore cursor position.
	if origErr == nil {
		var ox, oy int
		if n, _ := fmt.Sscanf(origOut, "x:%d y:%d", &ox, &oy); n == 2 {
			run("mousemove", strconv.Itoa(ox), strconv.Itoa(oy)) //nolint:errcheck
		}
	}

	return maxX + 1, maxY + 1, nil
}


// findBin looks for a binary in PATH and common NixOS system locations.
func findBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, p := range []string{
		"/run/current-system/sw/bin/" + name,
		"/nix/var/nix/profiles/default/bin/" + name,
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// slamToCorner creates a temporary uinput mouse and sends enough large negative
// deltas to guarantee the cursor is clamped at (0, 0).
func slamToCorner() error {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open /dev/uinput: %w", err)
	}
	defer func() {
		syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uiDevDestroy, 0)
		f.Close()
	}()

	ioc := func(req, arg uintptr) error {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, arg)
		if errno != 0 {
			return errno
		}
		return nil
	}

	// A minimal mouse: EV_REL + BTN_LEFT (so libinput classifies it as pointer).
	if err := ioc(uiSetEvBit, uintptr(evRel)); err != nil {
		return fmt.Errorf("SET_EVBIT EV_REL: %w", err)
	}
	if err := ioc(uiSetRelBit, uintptr(relX)); err != nil {
		return fmt.Errorf("SET_RELBIT REL_X: %w", err)
	}
	if err := ioc(uiSetRelBit, uintptr(relY)); err != nil {
		return fmt.Errorf("SET_RELBIT REL_Y: %w", err)
	}
	if err := ioc(uiSetEvBit, uintptr(evKey)); err != nil {
		return fmt.Errorf("SET_EVBIT EV_KEY: %w", err)
	}
	if err := ioc(uiSetKeyBit, uintptr(btnLeft)); err != nil {
		return fmt.Errorf("SET_KEYBIT BTN_LEFT: %w", err)
	}

	var dev uinputUserDev
	copy(dev.Name[:], "kvm-warp")
	dev.ID.Bustype = busUSB
	b := (*[unsafe.Sizeof(dev)]byte)(unsafe.Pointer(&dev))
	if _, err := f.Write(b[:]); err != nil {
		return fmt.Errorf("write uinput_user_dev: %w", err)
	}
	if err := ioc(uiDevCreate, 0); err != nil {
		return fmt.Errorf("DEV_CREATE: %w", err)
	}

	time.Sleep(100 * time.Millisecond)

	send := func(typ, code uint16, val int32) {
		ev := warpEvent{Type: typ, Code: code, Value: val}
		b := (*[unsafe.Sizeof(ev)]byte)(unsafe.Pointer(&ev))
		f.Write(b[:])
	}

	// Eight slams of -32767 guarantees we hit the corner regardless of speed.
	for range 8 {
		send(evRel, relX, -32767)
		send(evRel, relY, -32767)
		send(evSyn, synReport, 0)
	}
	time.Sleep(80 * time.Millisecond)
	return nil
}
