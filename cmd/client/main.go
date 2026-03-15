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

	"kvm-bodge/internal/proto"
)

func main() {
	server := flag.String("server", "", "server IP or host (required)")
	port := flag.Int("port", 7777, "server port")
	flag.Parse()

	if *server == "" {
		fmt.Fprintln(os.Stderr, "usage: client --server <ip> [--port <port>]")
		os.Exit(1)
	}

	addr := fmt.Sprintf("%s:%d", *server, *port)
	log.Printf("connecting to %s", addr)

	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// --- Handshake ---
	// 1. Expect server hello.
	msg, err := proto.Read(c)
	if err != nil {
		log.Fatalf("hello recv: %v", err)
	}
	if msg.Type != proto.MsgHello || string(msg.Payload) != proto.ServerHello {
		log.Fatalf("unexpected hello: type=%#x payload=%q", msg.Type, msg.Payload)
	}

	// 2. Send client hello.
	if err := proto.Write(c, proto.Message{
		Type:    proto.MsgHello,
		Payload: []byte(proto.ClientHello),
	}); err != nil {
		log.Fatalf("hello send: %v", err)
	}
	log.Printf("handshake OK — connected to %s", addr)

	// Catch Ctrl-C so we can send a clean Bye.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// --- Heartbeat loop ---
	for {
		c.SetDeadline(time.Now().Add(10 * time.Second))
		msg, err := proto.Read(c)
		if err != nil {
			// Check if we got a quit signal while blocked.
			select {
			case <-sig:
				sendBye(c)
				log.Println("bye")
				return
			default:
			}
			log.Printf("recv: %v", err)
			return
		}

		switch msg.Type {
		case proto.MsgHeartbeatPing:
			// Reply immediately.
			if err := proto.Write(c, proto.Message{Type: proto.MsgHeartbeatPong}); err != nil {
				log.Printf("pong send: %v", err)
				return
			}
			log.Println("heartbeat OK")

		case proto.MsgBye:
			log.Println("server said bye")
			return

		default:
			log.Printf("unexpected message type %#x", msg.Type)
			return
		}

		// Also check for quit between heartbeats.
		select {
		case <-sig:
			sendBye(c)
			log.Println("bye")
			return
		default:
		}
	}
}

func sendBye(c net.Conn) {
	c.SetDeadline(time.Now().Add(2 * time.Second))
	proto.Write(c, proto.Message{Type: proto.MsgBye}) //nolint:errcheck
}
