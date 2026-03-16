// Example: reading registers with a typed codec from the codec package.
//
// Usage:
//
//	go run ./examples/codec_read
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/codec"
)

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
	unitID := uint8(1)
	addr := uint16(0x0000)

	// Read a single uint32 from two holding registers using a typed codec.
	// codec.ReadFromClient takes any RegisterReader (which *Client satisfies).
	u32Codec := codec.MustNewUint32Codec(codec.Layout32_4321)
	val, err := codec.ReadFromClient(client, ctx, unitID, addr, codec.HoldingRegister, u32Codec)
	if err != nil {
		log.Fatalf("ReadFromClient: %v", err)
	}
	fmt.Printf("uint32 at 0x%04x: %v (0x%08x)\n", addr, val, val)

	// Read a float32 using the runtime codec registry.
	rc, ok, err := codec.RuntimeCodecByID("float32/layout:4321")
	if err != nil {
		log.Fatalf("RuntimeCodecByID: %v", err)
	}
	if !ok {
		log.Fatal("float32 codec not found")
	}
	decoded, err := codec.ReadRuntimeFromClient(client, ctx, unitID, addr, codec.HoldingRegister, rc)
	if err != nil {
		log.Fatalf("ReadRuntimeFromClient: %v", err)
	}
	fmt.Printf("float32 at 0x%04x: %v\n", addr, decoded)
}
