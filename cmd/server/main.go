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
	port := flag.Int("port", 7777, "TCP port to listen on")
	flag.Parse()

	addr := fmt.Sprintf(":%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	log.Printf("KVM server listening on %s", addr)

	// Accept connections in the background; shut down on signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	connCh := make(chan net.Conn)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			connCh <- c
		}
	}()

	for {
		select {
		case <-sig:
			log.Println("shutting down")
			return
		case c := <-connCh:
			go handleClient(c)
		}
	}
}

func handleClient(c net.Conn) {
	remote := c.RemoteAddr()
	log.Printf("[%s] connected", remote)
	defer func() {
		c.Close()
		log.Printf("[%s] disconnected", remote)
	}()

	// --- Handshake ---
	// 1. Send server hello.
	if err := proto.Write(c, proto.Message{
		Type:    proto.MsgHello,
		Payload: []byte(proto.ServerHello),
	}); err != nil {
		log.Printf("[%s] hello send: %v", remote, err)
		return
	}

	// 2. Expect client hello.
	msg, err := proto.Read(c)
	if err != nil {
		log.Printf("[%s] hello recv: %v", remote, err)
		return
	}
	if msg.Type != proto.MsgHello || string(msg.Payload) != proto.ClientHello {
		log.Printf("[%s] unexpected hello: type=%#x payload=%q", remote, msg.Type, msg.Payload)
		return
	}
	log.Printf("[%s] handshake OK", remote)

	// --- Heartbeat loop ---
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Send ping.
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if err := proto.Write(c, proto.Message{Type: proto.MsgHeartbeatPing}); err != nil {
			log.Printf("[%s] ping send: %v", remote, err)
			return
		}

		// Expect pong.
		resp, err := proto.Read(c)
		if err != nil {
			log.Printf("[%s] pong recv: %v", remote, err)
			return
		}
		if resp.Type == proto.MsgBye {
			log.Printf("[%s] client said bye", remote)
			return
		}
		if resp.Type != proto.MsgHeartbeatPong {
			log.Printf("[%s] unexpected message type %#x", remote, resp.Type)
			return
		}
		log.Printf("[%s] heartbeat OK", remote)
	}
}
