package modbus

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// --- FC07: ReadExceptionStatus -----------------------------------------------

func TestReadExceptionStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8) // MBAP header (7) + FC (1), no payload
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		_ = writeMBAPNormal(sock, txid, unitID, byte(FCReadExceptionStatus), []byte{0x6D})
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	status, err := client.ReadExceptionStatus(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReadExceptionStatus: %v", err)
	}
	if status != 0x6D {
		t.Errorf("status = 0x%02X, want 0x6D", status)
	}
}

func TestReadExceptionStatus_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		_ = writeMBAPException(sock, txid, unitID, byte(FCReadExceptionStatus), byte(exIllegalFunction))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadExceptionStatus(context.Background(), 1)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Errorf("want ErrIllegalFunction, got %v", err)
	}
}

func TestReadExceptionStatus_PayloadTooLong(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		_ = writeMBAPNormal(sock, txid, unitID, byte(FCReadExceptionStatus), []byte{0x6D, 0x00})
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadExceptionStatus(context.Background(), 1)
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

// --- FC0B: GetCommEventCounter -----------------------------------------------

func TestGetCommEventCounter(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		_ = writeMBAPNormal(sock, txid, unitID, byte(FCGetCommEventCounters), []byte{0xFF, 0xFF, 0x01, 0x08})
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	cr, err := client.GetCommEventCounter(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCommEventCounter: %v", err)
	}
	if cr.Status != 0xFFFF || cr.EventCount != 0x0108 {
		t.Errorf("Status=0x%04X EventCount=0x%04X, want 0xFFFF/0x0108", cr.Status, cr.EventCount)
	}
}

func TestGetCommEventCounter_PayloadTooShort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		_ = writeMBAPNormal(sock, txid, unitID, byte(FCGetCommEventCounters), []byte{0xFF, 0xFF})
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.GetCommEventCounter(context.Background(), 1)
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

// --- FC0C: GetCommEventLog ---------------------------------------------------

func TestGetCommEventLog(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		// ByteCount=8, Status=0x0000, EventCount=0x0108, MessageCount=0x0121, Event0=0x20, Event1=0x00
		payload := []byte{0x08, 0x00, 0x00, 0x01, 0x08, 0x01, 0x21, 0x20, 0x00}
		_ = writeMBAPNormal(sock, txid, unitID, byte(FCGetCommEventLog), payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	cl, err := client.GetCommEventLog(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCommEventLog: %v", err)
	}
	if cl.Status != 0x0000 || cl.EventCount != 0x0108 || cl.MessageCount != 0x0121 {
		t.Errorf("Status=0x%04X EventCount=0x%04X MessageCount=0x%04X", cl.Status, cl.EventCount, cl.MessageCount)
	}
	if len(cl.Events) != 2 || cl.Events[0] != 0x20 || cl.Events[1] != 0x00 {
		t.Errorf("Events=%v, want [0x20 0x00]", cl.Events)
	}
}

func TestGetCommEventLog_ByteCountMismatch(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		// ByteCount=10 but only 8 bytes of data follow
		payload := []byte{0x0A, 0x00, 0x00, 0x01, 0x08, 0x01, 0x21, 0x20, 0x00}
		_ = writeMBAPNormal(sock, txid, unitID, byte(FCGetCommEventLog), payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.GetCommEventLog(context.Background(), 1)
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

func TestGetCommEventLog_ByteCountTooSmall(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		// ByteCount=4, only has room for Status + partial EventCount
		payload := []byte{0x04, 0x00, 0x00, 0x01, 0x08}
		_ = writeMBAPNormal(sock, txid, unitID, byte(FCGetCommEventLog), payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.GetCommEventLog(context.Background(), 1)
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

func TestGetCommEventLog_NoEvents(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		req := make([]byte, 8)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		// ByteCount=6, no events
		payload := []byte{0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		_ = writeMBAPNormal(sock, txid, unitID, byte(FCGetCommEventLog), payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	cl, err := client.GetCommEventLog(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCommEventLog: %v", err)
	}
	if len(cl.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(cl.Events))
	}
}
