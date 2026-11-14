// SPDX-License-Identifier: MIT

package adu

import (
	"fmt"
	"testing"
)

// Microbenchmarks of the three frame decoders: CPU only, no I/O, so that a
// change to a framer can be compared before and after without the noise of a
// socket (the end-to-end benchmarks of the root package run over loopback).
// Each one checks the decode it times and keeps its result in a package-level
// sink.

var (
	sinkInt     int
	sinkByte    byte
	sinkPayload []byte
)

// benchPayloads are the payload sizes of the frames decoded: the response to a
// read of one register, of sixteen, and the largest a frame carries.
var benchPayloads = []int{3, 33, 252}

func benchPayload(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i)
	}
	if n > 0 {
		p[0] = byte(n - 1)
	}
	return p
}

// BenchmarkDecodeMBAP measures the decoding of one MBAP header.
func BenchmarkDecodeMBAP(b *testing.B) {
	for _, n := range benchPayloads {
		b.Run(fmt.Sprintf("payload=%d", n), func(b *testing.B) {
			header := AssembleMBAP(0x1234, 0x11, 0x03, benchPayload(n))[:MBAPHeaderLength]
			b.ReportAllocs()
			b.SetBytes(int64(len(header)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				txnID, unitID, mbapLen, err := ParseMBAPHeader(header)
				if err != nil || mbapLen != n+2 {
					b.Fatalf("ParseMBAPHeader: length %d, %v", mbapLen, err)
				}
				sinkInt = mbapLen + int(txnID)
				sinkByte = unitID
			}
		})
	}
}

// BenchmarkDecodeRTU measures the CRC check and the split of one RTU frame.
func BenchmarkDecodeRTU(b *testing.B) {
	for _, n := range benchPayloads {
		b.Run(fmt.Sprintf("payload=%d", n), func(b *testing.B) {
			frame := AssembleRTUFrame(0x11, 0x03, benchPayload(n))
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !ValidateRTUCRC(frame) {
					b.Fatal("ValidateRTUCRC rejected a valid frame")
				}
				unitID, fc, payload := ParseRTUFrame(frame)
				if len(payload) != n {
					b.Fatalf("ParseRTUFrame: %d payload bytes, want %d", len(payload), n)
				}
				sinkByte = unitID + fc
				sinkPayload = payload
			}
		})
	}
}

// BenchmarkDecodeASCII measures the decoding of one ASCII frame: the hex
// characters and the LRC.
func BenchmarkDecodeASCII(b *testing.B) {
	for _, n := range benchPayloads {
		b.Run(fmt.Sprintf("payload=%d", n), func(b *testing.B) {
			frame := AssembleASCIIFrame(0x11, 0x03, benchPayload(n))
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				unitID, fc, payload, err := ParseASCIIFrame(frame)
				if err != nil || len(payload) != n {
					b.Fatalf("ParseASCIIFrame: %d payload bytes, %v", len(payload), err)
				}
				sinkByte = unitID + fc
				sinkPayload = payload
			}
		})
	}
}
