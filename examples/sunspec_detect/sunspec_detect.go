// SPDX-License-Identifier: MIT

// Example: SunSpec marker detection using the sunspec package directly.
//
// Usage:
//
//	go run ./examples/sunspec_detect
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/sunspec"
)

// adapter satisfies sunspec.Reader using a ModbusClient.
type adapter struct {
	client *modbus.Client
}

func (a *adapter) ReadRawBytes(ctx context.Context, unitID uint8, addr uint16, byteCount uint16, regType sunspec.RegType) ([]byte, error) {
	return a.client.ReadRegisterBytes(ctx, unitID, addr, byteCount, regType)
}

func main() {
	target := "tcp://localhost:502"
	if len(os.Args) > 1 {
		target = os.Args[1]
	}

	client, err := modbus.New(modbus.Config{URL: target})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		log.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	reader := &adapter{client: client}

	opts := &sunspec.Options{
		UnitID:  1,
		RegType: sunspec.HoldingRegister,
	}

	result, err := sunspec.Detect(ctx, reader, opts)
	if err != nil {
		log.Fatalf("Detect: %v", err)
	}
	if !result.Detected {
		fmt.Println("SunSpec marker not found")
		return
	}
	fmt.Printf("SunSpec marker found at address %v (register type: %v)\n",
		result.BaseAddress, result.RegType)

	discovery, err := sunspec.Discover(ctx, reader, opts)
	if err != nil {
		log.Fatalf("Discover: %v", err)
	}
	fmt.Printf("Found %d SunSpec model(s):\n", len(discovery.Models))
	for _, h := range discovery.Models {
		if h.IsEndModel {
			continue
		}
		fmt.Printf("  Model %d: %d registers at address %d\n", h.ID, h.Length, h.StartAddress)
	}
}
