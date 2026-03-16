package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"sync"
)

// Status is the state broadcast to all monitor connections.
type Status struct {
	Connected bool   `json:"connected"`
	Remote    bool   `json:"remote"`
	Client    string `json:"client,omitempty"`
}

// statusBroadcaster fans out status updates to any number of Unix socket listeners.
type statusBroadcaster struct {
	mu      sync.Mutex
	current Status
	conns   []net.Conn
}

func newStatusBroadcaster() *statusBroadcaster {
	return &statusBroadcaster{}
}

// publish sends the new status to all connected monitors and caches it.
func (b *statusBroadcaster) publish(s Status) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.current = s
	line, _ := json.Marshal(s)
	line = append(line, '\n')
	var alive []net.Conn
	for _, c := range b.conns {
		if _, err := c.Write(line); err == nil {
			alive = append(alive, c)
		} else {
			c.Close()
		}
	}
	b.conns = alive
}

// serve listens on socketPath and sends the current status to each new connection,
// then keeps it in the fan-out list. Runs until ctx is cancelled.
func (b *statusBroadcaster) serve(ctx context.Context, socketPath string) {
	os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Printf("status socket: %v", err)
		return
	}
	if err := os.Chmod(socketPath, 0666); err != nil {
		log.Printf("status socket chmod: %v", err)
	}
	log.Printf("status socket: %s", socketPath)

	go func() {
		<-ctx.Done()
		ln.Close()
		os.Remove(socketPath)
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		line, _ := json.Marshal(b.current)
		line = append(line, '\n')
		c.Write(line)
		b.conns = append(b.conns, c)
		b.mu.Unlock()
	}
}
