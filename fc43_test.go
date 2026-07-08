// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestReadDeviceIdentification_InvalidReadDeviceIdCode(t *testing.T) {
	client, err := New(Config{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = client.Open()
	defer func() { _ = client.Close() }()
	ctx := context.Background()

	_, err = client.ReadDeviceIdentification(ctx, 1, 0, 0x00)
	if err == nil {
		t.Fatal("readDeviceIdCode 0 should error")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("got %v", err)
	}

	_, err = client.ReadDeviceIdentification(ctx, 1, 5, 0x00)
	if err == nil {
		t.Fatal("readDeviceIdCode 5 should error")
	}
}

func TestReadDeviceIdentification_Exception(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		txid, unitID, fc := req[0:2], req[6], req[7]
		_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
	if err == nil {
		t.Fatal("expected exception error")
	}
	if !errors.Is(err, ErrIllegalFunction) {
		t.Errorf("want ErrIllegalFunction, got %v", err)
	}
}

func TestReadDeviceIdentification_ProtocolError_PayloadTooShort(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		// Payload < 6 bytes
		payload := []byte{byte(MEIReadDeviceIdentification), ReadDeviceIDBasic, 0x01, 0x00}
		_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
	if err == nil {
		t.Fatal("expected protocol error")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

func TestReadDeviceIdentification_ProtocolError_WrongMEI(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		// Wrong MEI type in response (0x00 instead of MEIReadDeviceIdentification)
		payload := []byte{0x00, ReadDeviceIDBasic, 0x01, 0x00, 0x00, 0x00}
		_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
	if err == nil {
		t.Fatal("expected protocol error")
	}
}

func TestReadDeviceIdentification_MoreFollows(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	reqCount := 0
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			req := make([]byte, 11)
			if _, err := io.ReadFull(sock, req); err != nil {
				return
			}
			if req[7] != byte(FCEncapsulatedInterface) {
				return
			}
			txid, unitID := req[0:2], req[6]
			reqCount++
			if reqCount == 1 {
				payload := []byte{
					byte(MEIReadDeviceIdentification), ReadDeviceIDBasic,
					0x01, 0xff, 0x02, 0x01,
					0x00, 0x02, 'A', 'B',
				}
				_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
			} else {
				payload := []byte{
					byte(MEIReadDeviceIdentification), ReadDeviceIDBasic,
					0x01, 0x00, 0x00, 0x01,
					0x02, 0x02, 0x00, 0x01,
				}
				_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
				return
			}
		}
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	di, err := client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
	if err != nil {
		t.Fatalf("ReadDeviceIdentification: %v", err)
	}
	if len(di.Objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(di.Objects))
	}
	if di.Objects[0].ID != 0x00 || di.Objects[0].Value != "AB" {
		t.Errorf("first object: ID=%d Value=%q", di.Objects[0].ID, di.Objects[0].Value)
	}
	if di.Objects[1].ID != 0x02 || di.Objects[1].Name != "MajorMinorRevision" {
		t.Errorf("second object: ID=%d Name=%q", di.Objects[1].ID, di.Objects[1].Name)
	}
}

func TestReadAllDeviceIdentification_Extended(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		if req[7] != byte(FCEncapsulatedInterface) || req[9] != ReadDeviceIDExtended {
			return
		}
		txid, unitID := req[0:2], req[6]
		payload := []byte{
			byte(MEIReadDeviceIdentification),
			ReadDeviceIDExtended,
			0x01, 0x00, 0x00,
			0x01,
			0x00, 0x02, 'A', 'B',
		}
		_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	di, err := client.ReadAllDeviceIdentification(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReadAllDeviceIdentification: %v", err)
	}
	if di.Category != DeviceIDExtended || len(di.Objects) != 1 {
		t.Errorf("Category=%d len(Objects)=%d", di.Category, len(di.Objects))
	}
}

func TestReadDeviceIdentification_ThreeObjects(t *testing.T) {
	// Covers objectDescription for object IDs 0x00, 0x01, 0x02 (MajorMinorRevision).
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		if req[7] != byte(FCEncapsulatedInterface) || req[9] != ReadDeviceIDBasic {
			return
		}
		txid, unitID := req[0:2], req[6]
		payload := []byte{
			byte(MEIReadDeviceIdentification),
			ReadDeviceIDBasic,
			0x01, 0x00, 0x00,
			0x03,
			0x00, 0x03, 'A', 'C', 'M',
			0x01, 0x05, 'P', '1', '2', '3', '4',
			0x02, 0x02, 0x00, 0x01,
		}
		_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	di, err := client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
	if err != nil {
		t.Fatalf("ReadDeviceIdentification: %v", err)
	}
	if len(di.Objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(di.Objects))
	}
	if di.Objects[2].ID != 0x02 || di.Objects[2].Name != "MajorMinorRevision" {
		t.Errorf("object 2: ID=%d Name=%q", di.Objects[2].ID, di.Objects[2].Name)
	}
}

func TestReadDeviceIdentification(t *testing.T) {
	var err error
	var ln net.Listener
	var client *Client
	var di *DeviceIdentification

	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		var err error
		var sock net.Conn
		var req []byte
		var payload []byte
		var txid []byte
		var unitID byte

		sock, err = ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = sock.Close() }()

		req = make([]byte, 11)
		_, err = io.ReadFull(sock, req)
		if err != nil {
			return
		}

		if req[2] != 0x00 || req[3] != 0x00 ||
			req[4] != 0x00 || req[5] != 0x05 ||
			req[7] != byte(FCEncapsulatedInterface) ||
			req[8] != byte(MEIReadDeviceIdentification) ||
			req[9] != ReadDeviceIDBasic || req[10] != 0x00 {
			return
		}

		txid = req[0:2]
		unitID = req[6]

		payload = []byte{
			byte(MEIReadDeviceIdentification),
			ReadDeviceIDBasic,
			0x01,
			0x00,
			0x00,
			0x02,
			0x00, 0x03, 'A', 'C', 'M',
			0x01, 0x05, 'P', '1', '2', '3', '4',
		}

		_, _ = sock.Write(append([]byte{
			txid[0], txid[1],
			0x00, 0x00,
			0x00, byte(2 + len(payload)),
			unitID,
			byte(FCEncapsulatedInterface),
		}, payload...))
	}()

	client, err = New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	err = client.Open()
	if err != nil {
		t.Fatalf("failed to open client: %v", err)
	}
	defer func() { _ = client.Close() }()

	di, err = client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
	if err != nil {
		t.Fatalf("ReadDeviceIdentification() should have succeeded, got: %v", err)
	}

	if di.Category != DeviceIDBasic || di.ConformityLevel != 0x01 ||
		di.MoreFollows || di.NextObjectID != 0x00 {
		t.Fatalf("unexpected FC43 header fields: %#v", di)
	}

	if len(di.Objects) != 2 {
		t.Fatalf("expected 2 objects, got: %v", len(di.Objects))
	}

	if di.Objects[0].ID != 0x00 || di.Objects[0].Name != "VendorName" || di.Objects[0].Value != "ACM" {
		t.Fatalf("unexpected first object: %#v", di.Objects[0])
	}

	if di.Objects[1].ID != 0x01 || di.Objects[1].Name != "ProductCode" || di.Objects[1].Value != "P1234" {
		t.Fatalf("unexpected second object: %#v", di.Objects[1])
	}
}

func TestReadDeviceIdentificationException(t *testing.T) {
	var err error
	var ln net.Listener
	var client *Client

	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		var err error
		var sock net.Conn
		var req []byte
		var txid []byte
		var unitID byte

		sock, err = ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = sock.Close() }()

		req = make([]byte, 11)
		_, err = io.ReadFull(sock, req)
		if err != nil {
			return
		}

		txid = req[0:2]
		unitID = req[6]

		_, _ = sock.Write([]byte{
			txid[0], txid[1],
			0x00, 0x00,
			0x00, 0x03,
			unitID,
			byte(FCEncapsulatedInterface) | 0x80,
			byte(exIllegalFunction),
		})
	}()

	client, err = New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	err = client.Open()
	if err != nil {
		t.Fatalf("failed to open client: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Fatalf("expected %v, got: %v", ErrIllegalFunction, err)
	}
}

func TestReadDeviceIdentificationRejectsUnexpectedCode(t *testing.T) {
	var err error
	var client *Client

	client, err = New(Config{URL: "tcp://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	_, err = client.ReadDeviceIdentification(context.Background(), 1, 0x00, 0x00)
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Fatalf("expected %v, got: %v", ErrUnexpectedParameters, err)
	}
}

func TestReadDeviceIdentification_InvalidConformityLevel(t *testing.T) {
	for _, badLevel := range []byte{0x00, 0x04, 0x80, 0x84, 0xFF} {
		t.Run(fmt.Sprintf("0x%02X", badLevel), func(t *testing.T) {
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
				req := make([]byte, 11)
				_, _ = io.ReadFull(sock, req)
				txid, unitID := req[0:2], req[6]
				payload := []byte{
					byte(MEIReadDeviceIdentification), ReadDeviceIDBasic,
					badLevel, 0x00, 0x00, 0x01,
					0x00, 0x03, 'A', 'C', 'M',
				}
				_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
			}()
			client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if err := client.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = client.Close() }()
			_, err = client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
			if err == nil {
				t.Fatal("expected protocol error for invalid conformity level")
			}
			if !errors.Is(err, ErrProtocolError) {
				t.Errorf("want ErrProtocolError, got %v", err)
			}
		})
	}
}

func TestReadDeviceIdentification_ValidConformityLevels(t *testing.T) {
	for _, level := range []byte{0x01, 0x02, 0x03, 0x81, 0x82, 0x83} {
		t.Run(fmt.Sprintf("0x%02X", level), func(t *testing.T) {
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
				req := make([]byte, 11)
				_, _ = io.ReadFull(sock, req)
				txid, unitID := req[0:2], req[6]
				payload := []byte{
					byte(MEIReadDeviceIdentification), ReadDeviceIDBasic,
					level, 0x00, 0x00, 0x01,
					0x00, 0x03, 'A', 'C', 'M',
				}
				_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
			}()
			client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if err := client.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = client.Close() }()
			di, err := client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if di.ConformityLevel != level {
				t.Errorf("conformity level: got 0x%02X, want 0x%02X", di.ConformityLevel, level)
			}
		})
	}
}

func TestReadDeviceIdentification_MoreFollowsZero_NextObjectIDNonZero(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		payload := []byte{
			byte(MEIReadDeviceIdentification), ReadDeviceIDBasic,
			0x01, 0x00, 0x05, 0x01, // MoreFollows=0x00 but NextObjectID=0x05
			0x00, 0x03, 'A', 'C', 'M',
		}
		_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x00)
	if err == nil {
		t.Fatal("expected error for MoreFollows=0x00 with NextObjectID != 0x00")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

func TestReadDeviceIdentification_Individual_MoreFollowsFF(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		payload := []byte{
			byte(MEIReadDeviceIdentification), ReadDeviceIDIndividual,
			0x81, 0xFF, 0x01, 0x01, // MoreFollows=0xFF (invalid for individual)
			0x00, 0x03, 'A', 'C', 'M',
		}
		_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadDeviceIdentification(context.Background(), 1, DeviceIDIndividual, 0x00)
	if err == nil {
		t.Fatal("expected error for individual access with MoreFollows=0xFF")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

func TestReadDeviceIdentification_Individual_MultipleObjects(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		payload := []byte{
			byte(MEIReadDeviceIdentification), ReadDeviceIDIndividual,
			0x81, 0x00, 0x00, 0x02, // NumberOfObjects=2 (invalid for individual)
			0x00, 0x02, 'A', 'B',
			0x01, 0x02, 'C', 'D',
		}
		_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadDeviceIdentification(context.Background(), 1, DeviceIDIndividual, 0x00)
	if err == nil {
		t.Fatal("expected error for individual access with NumberOfObjects != 1")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

func TestReadDeviceIdentification_StreamAccess_UnknownObjectID_RestartsFromZero(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		// Client requests startObject=0x80 (unknown). Server restarts from 0x00.
		payload := []byte{
			byte(MEIReadDeviceIdentification), ReadDeviceIDBasic,
			0x01, 0x00, 0x00, 0x01,
			0x00, 0x03, 'A', 'C', 'M', // object 0x00 returned despite 0x80 requested
		}
		_, _ = sock.Write(append([]byte{txid[0], txid[1], 0x00, 0x00, 0x00, byte(2 + len(payload)), unitID, byte(FCEncapsulatedInterface)}, payload...))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	di, err := client.ReadDeviceIdentification(context.Background(), 1, ReadDeviceIDBasic, 0x80)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(di.Objects) != 1 || di.Objects[0].ID != 0x00 {
		t.Errorf("expected 1 object with ID 0x00, got %v", di.Objects)
	}
}

func TestReadDeviceIdentification_IndividualAccess_UnknownObjectID_Exception02(t *testing.T) {
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
		req := make([]byte, 11)
		_, _ = io.ReadFull(sock, req)
		txid, unitID := req[0:2], req[6]
		_ = writeMBAPException(sock, txid, unitID, byte(FCEncapsulatedInterface), byte(exIllegalDataAddress))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadDeviceIdentification(context.Background(), 1, DeviceIDIndividual, 0xFF)
	if !errors.Is(err, ErrIllegalDataAddress) {
		t.Errorf("want ErrIllegalDataAddress, got %v", err)
	}
}

func TestDeviceIdentification_SupportsStreamAndIndividualAccess(t *testing.T) {
	tests := []struct {
		level          uint8
		wantStream     bool
		wantIndividual bool
	}{
		{0x01, true, false},
		{0x02, true, false},
		{0x03, true, false},
		{0x81, true, true},
		{0x82, true, true},
		{0x83, true, true},
		{0x00, false, false},
		{0x04, false, false},
	}
	for _, tt := range tests {
		di := &DeviceIdentification{ConformityLevel: tt.level}
		if got := di.SupportsStreamAccess(); got != tt.wantStream {
			t.Errorf("ConformityLevel=0x%02X: SupportsStreamAccess()=%v, want %v", tt.level, got, tt.wantStream)
		}
		if got := di.SupportsIndividualAccess(); got != tt.wantIndividual {
			t.Errorf("ConformityLevel=0x%02X: SupportsIndividualAccess()=%v, want %v", tt.level, got, tt.wantIndividual)
		}
	}
}

// TestReadAllDeviceIdentification verifies that ReadAllDeviceIdentification requests
// Extended (0x03) and returns all objects the device reports (basic + regular + extended).
func TestReadAllDeviceIdentification(t *testing.T) {
	var err error
	var ln net.Listener
	var client *Client
	var di *DeviceIdentification

	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test listener: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		var err error
		var sock net.Conn
		var req []byte
		var payload []byte
		var txid []byte
		var unitID byte

		sock, err = ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = sock.Close() }()

		req = make([]byte, 11)
		_, err = io.ReadFull(sock, req)
		if err != nil {
			return
		}

		// ReadAllDeviceIdentification sends readDeviceIdCode 0x03 (Extended), objectId 0x00
		if req[2] != 0x00 || req[3] != 0x00 ||
			req[4] != 0x00 || req[5] != 0x05 ||
			req[7] != byte(FCEncapsulatedInterface) ||
			req[8] != byte(MEIReadDeviceIdentification) ||
			req[9] != ReadDeviceIDExtended || req[10] != 0x00 {
			return
		}

		txid = req[0:2]
		unitID = req[6]

		// Simulate device that supports regular: basic (0x00–0x02) + VendorUrl (0x03), ProductName (0x04)
		payload = []byte{
			byte(MEIReadDeviceIdentification),
			ReadDeviceIDExtended,
			0x02, // conformity level: regular
			0x00, 0x00,
			0x05, // number of objects
			0x00, 0x03, 'A', 'C', 'M',
			0x01, 0x05, 'P', '1', '2', '3', '4',
			0x02, 0x03, '1', '.', '0',
			0x03, 0x09, 'h', 't', 't', 'p', 's', ':', '/', '/', 'x',
			0x04, 0x06, 'M', 'y', 'P', 'r', 'o', 'd',
		}

		_, _ = sock.Write(append([]byte{
			txid[0], txid[1],
			0x00, 0x00,
			0x00, byte(2 + len(payload)),
			unitID,
			byte(FCEncapsulatedInterface),
		}, payload...))
	}()

	client, err = New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	err = client.Open()
	if err != nil {
		t.Fatalf("failed to open client: %v", err)
	}
	defer func() { _ = client.Close() }()

	di, err = client.ReadAllDeviceIdentification(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReadAllDeviceIdentification() should have succeeded, got: %v", err)
	}

	if di.Category != DeviceIDExtended || di.ConformityLevel != 0x02 ||
		di.MoreFollows || di.NextObjectID != 0x00 {
		t.Fatalf("unexpected FC43 header: Category=%v ConformityLevel=%v MoreFollows=%v NextObjectID=%v",
			di.Category, di.ConformityLevel, di.MoreFollows, di.NextObjectID)
	}

	if len(di.Objects) != 5 {
		t.Fatalf("expected 5 objects (basic + regular), got: %v", len(di.Objects))
	}

	if di.Objects[0].ID != 0x00 || di.Objects[0].Name != "VendorName" || di.Objects[0].Value != "ACM" {
		t.Fatalf("object 0: got %#v", di.Objects[0])
	}
	if di.Objects[1].ID != 0x01 || di.Objects[1].Name != "ProductCode" || di.Objects[1].Value != "P1234" {
		t.Fatalf("object 1: got %#v", di.Objects[1])
	}
	if di.Objects[2].ID != 0x02 || di.Objects[2].Name != "MajorMinorRevision" || di.Objects[2].Value != "1.0" {
		t.Fatalf("object 2: got %#v", di.Objects[2])
	}
	if di.Objects[3].ID != 0x03 || di.Objects[3].Name != "VendorUrl" || di.Objects[3].Value != "https://x" {
		t.Fatalf("object 3: got %#v", di.Objects[3])
	}
	if di.Objects[4].ID != 0x04 || di.Objects[4].Name != "ProductName" || di.Objects[4].Value != "MyProd" {
		t.Fatalf("object 4: got %#v", di.Objects[4])
	}
}
