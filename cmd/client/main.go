package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-vgo/robotgo"

	"kvm-bodge/internal/proto"
)

var debug bool

func dbg(format string, args ...any) {
	if debug {
		log.Printf("[debug] "+format, args...)
	}
}

func main() {
	server := flag.String("server", "", "server IP or host (required)")
	port := flag.Int("port", 7777, "server port")
	sideStr := flag.String("side", "", "which side of the server this monitor is on: left|right|top|bottom (required)")
	scrollSpeed := flag.Int("scroll-speed", 5, "scroll wheel multiplier")
	flag.BoolVar(&debug, "debug", false, "verbose debug output")
	flag.Parse()

	if *server == "" || *sideStr == "" {
		fmt.Fprintln(os.Stderr, "usage: client --server <ip> --side <left|right|top|bottom> [--port <port>]")
		os.Exit(1)
	}

	side, err := proto.SideFromString(*sideStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	addr := fmt.Sprintf("%s:%d", *server, *port)
	log.Printf("connecting to %s (this monitor is to the %s of server)", addr, *sideStr)

	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// --- Handshake ---
	msg, err := proto.Read(c)
	if err != nil || msg.Type != proto.MsgHello || string(msg.Payload) != proto.ServerHello {
		log.Fatalf("bad server hello")
	}
	if err := proto.Write(c, proto.Message{
		Type:    proto.MsgHello,
		Payload: []byte(proto.ClientHello),
	}); err != nil {
		log.Fatalf("hello send: %v", err)
	}
	// Send side info.
	if err := proto.Write(c, proto.Message{
		Type:    proto.MsgClientInfo,
		Payload: []byte{side},
	}); err != nil {
		log.Fatalf("client info send: %v", err)
	}
	log.Printf("handshake OK — connected to %s", addr)

	// --- Set up goroutines ---
	writeCh := make(chan proto.Message, 128)
	errCh := make(chan error, 4)

	go func() {
		for msg := range writeCh {
			if err := proto.Write(c, msg); err != nil {
				errCh <- err
				return
			}
		}
	}()

	inCh := make(chan proto.Message, 32)
	go func() {
		for {
			m, err := proto.Read(c)
			if err != nil {
				errCh <- err
				return
			}
			inCh <- m
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	screenW, screenH := robotgo.GetScreenSize()
	log.Printf("screen size %dx%d", screenW, screenH)

	// vx, vy track our own virtual cursor position — same approach as the server.
	// We don't rely on robotgo.GetMousePos() because it can be unreliable and
	// robotgo.Move() requires Accessibility permission on macOS (grant it in
	// System Settings → Privacy & Security → Accessibility).
	vx, vy := screenW/2, screenH/2
	remoteMode := false
	pressedButtons := map[uint16]bool{}

	for {
		select {
		case <-sig:
			writeCh <- proto.Message{Type: proto.MsgBye}
			time.Sleep(100 * time.Millisecond)
			log.Println("bye")
			return

		case err := <-errCh:
			log.Printf("error: %v", err)
			return

		case m := <-inCh:
			switch m.Type {
			case proto.MsgHeartbeatPing:
				writeCh <- proto.Message{Type: proto.MsgHeartbeatPong}

			case proto.MsgMouseEnter:
				remoteMode = true
				if len(m.Payload) >= 2 {
					pct := proto.DecodeEdgePos(m.Payload)
					vx, vy = entryPosFromPct(side, screenW, screenH, pct)
				} else {
					vx, vy = entryPos(side, screenW, screenH)
				}
				moveMouse(vx, vy, false)
				log.Printf("mouse entered from server — placed at (%d,%d)", vx, vy)

			case proto.MsgMouseDelta:
				if !remoteMode || len(m.Payload) < 8 {
					continue
				}
				dx, dy, wv, wh := proto.DecodeMouseDelta(m.Payload)
				vx = clamp(vx+dx, 0, screenW-1)
				vy = clamp(vy+dy, 0, screenH-1)
				moveMouse(vx, vy, pressedButtons[0x110])
				if wv != 0 {
					robotgo.Scroll(0, -wv**scrollSpeed)
				}
				if wh != 0 {
					robotgo.Scroll(wh**scrollSpeed, 0)
				}
				dbg("delta (%+d,%+d) scroll(%+d,%+d) → virtual (%d,%d)", dx, dy, wv, wh, vx, vy)

				if atReturnEdge(vx, vy, dx, dy, side, screenW, screenH) && len(pressedButtons) == 0 {
					remoteMode = false
					pct := edgePosPct(vx, vy, side, screenW, screenH)
					writeCh <- proto.Message{Type: proto.MsgMouseLeave, Payload: proto.EncodeEdgePos(pct)}
					log.Printf("return edge — mouse back to server (edge pos %.1f%%)", pct*100)
				}

			case proto.MsgMouseButton:
				if !remoteMode || len(m.Payload) < 3 {
					continue
				}
				button, pressed := proto.DecodeMouseButton(m.Payload)
				if pressed {
					pressedButtons[button] = true
				} else {
					delete(pressedButtons, button)
				}
				btn := evdevButtonToRobotgo(button)
				if btn == "" {
					continue
				}
				dbg("button %s pressed=%v", btn, pressed)
				if pressed {
					robotgo.MouseDown(btn)
				} else {
					robotgo.MouseUp(btn)
				}

			case proto.MsgBye:
				log.Println("server said bye")
				return
			}
		}
	}
}

// entryPosFromPct places the cursor at the same relative position along the
// entry edge as the server's cursor was along the exit edge.
func entryPosFromPct(side byte, w, h int, pct float64) (x, y int) {
	switch side {
	case proto.SideRight: // client is right → mouse enters from left
		return 2, int(pct * float64(h-1))
	case proto.SideLeft: // client is left → mouse enters from right
		return w - 2, int(pct * float64(h-1))
	case proto.SideTop: // client is above → mouse enters from bottom
		return int(pct * float64(w-1)), h - 2
	case proto.SideBottom: // client is below → mouse enters from top
		return int(pct * float64(w-1)), 2
	}
	return w / 2, h / 2
}

// entryPos falls back to center when no percentage is available.
func entryPos(side byte, w, h int) (x, y int) {
	return entryPosFromPct(side, w, h, 0.5)
}

// edgePosPct returns 0.0–1.0 for where along the crossing edge the cursor sits.
func edgePosPct(vx, vy int, side byte, w, h int) float64 {
	switch side {
	case proto.SideRight, proto.SideLeft:
		if h <= 1 {
			return 0.5
		}
		return float64(vy) / float64(h-1)
	case proto.SideTop, proto.SideBottom:
		if w <= 1 {
			return 0.5
		}
		return float64(vx) / float64(w-1)
	}
	return 0.5
}

// atReturnEdge returns true when the virtual position is clamped at the return
// edge and the incoming delta is still pushing toward it (push-through).
func atReturnEdge(x, y, dx, dy int, side byte, w, h int) bool {
	switch side {
	case proto.SideRight: // entered from left → return when pushed back left
		return x == 0 && dx < 0
	case proto.SideLeft: // entered from right → return when pushed back right
		return x == w-1 && dx > 0
	case proto.SideTop: // entered from bottom → return when pushed back down
		return y == h-1 && dy > 0
	case proto.SideBottom: // entered from top → return when pushed back up
		return y == 0 && dy < 0
	}
	return false
}

func evdevButtonToRobotgo(code uint16) string {
	switch code {
	case 0x110:
		return "left"
	case 0x111:
		return "right"
	case 0x112:
		return "center"
	}
	return ""
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
