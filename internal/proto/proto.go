// Package proto defines the KVM wire protocol.
//
// Frame format:
//
//	[1 byte: MsgType] [2 bytes: payload length, big-endian] [N bytes: payload]
package proto

import (
	"encoding/binary"
	"fmt"
	"io"
)

type MsgType byte

const (
	MsgHello         MsgType = 0x01
	MsgHeartbeatPing MsgType = 0x02
	MsgHeartbeatPong MsgType = 0x03
	MsgBye           MsgType = 0x04
)

const (
	ServerHello = "KVM-SERVER/1.0"
	ClientHello = "KVM-CLIENT/1.0"
)

type Message struct {
	Type    MsgType
	Payload []byte
}

func Write(w io.Writer, msg Message) error {
	if len(msg.Payload) > 0xFFFF {
		return fmt.Errorf("payload too large: %d bytes", len(msg.Payload))
	}
	buf := make([]byte, 3+len(msg.Payload))
	buf[0] = byte(msg.Type)
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(msg.Payload)))
	copy(buf[3:], msg.Payload)
	_, err := w.Write(buf)
	return err
}

func Read(r io.Reader) (Message, error) {
	hdr := make([]byte, 3)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return Message{}, err
	}
	msgType := MsgType(hdr[0])
	payloadLen := binary.BigEndian.Uint16(hdr[1:3])
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Message{}, err
		}
	}
	return Message{Type: msgType, Payload: payload}, nil
}
