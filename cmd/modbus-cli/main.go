package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/otfabric/go-modbus"
)

func main() {
	var err error
	var help bool
	var jsonOutput bool
	var failFast bool
	var client *modbus.Client
	var config modbus.Config
	var target string
	var caPath string
	var certPath string
	var keyPath string
	var clientKeyPair tls.Certificate
	var speed uint
	var dataBits uint
	var parity string
	var stopBits uint
	var endianness string
	var wordOrder string
	var timeout string
	var unitID uint

	flag.StringVar(&target, "target", "", "target device to connect to (e.g. tcp://somehost:502) [required]")
	flag.UintVar(&speed, "speed", 19200, "serial bus speed in bps (rtu)")
	flag.UintVar(&dataBits, "data-bits", 8, "number of bits per character on the serial bus (rtu)")
	flag.StringVar(&parity, "parity", "none", "parity bit <none|even|odd> on the serial bus (rtu)")
	flag.UintVar(&stopBits, "stop-bits", 0, "number of stop bits <0|1|2> on the serial bus (rtu, 0 = auto based on parity)")
	flag.StringVar(&timeout, "timeout", "3s", "timeout value")
	flag.StringVar(&endianness, "endianness", "big", "register endianness <little|big>")
	flag.StringVar(&wordOrder, "word-order", "highfirst", "word ordering for 32-bit registers <highfirst|hf|lowfirst|lf>")
	flag.UintVar(&unitID, "unit-id", 1, "unit/slave id to use")
	flag.StringVar(&certPath, "cert", "", "path to TLS client certificate")
	flag.StringVar(&keyPath, "key", "", "path to TLS client key")
	flag.StringVar(&caPath, "ca", "", "path to TLS CA/server certificate")
	flag.BoolVar(&jsonOutput, "json", false, "output results as line-delimited JSON (one JSON object per result)")
	flag.BoolVar(&failFast, "fail-fast", false, "stop execution on first operation error")
	flag.BoolVar(&help, "help", false, "show a wall-of-text help message")
	flag.Parse()

	if help {
		displayHelp()
		os.Exit(0)
	}

	if target == "" {
		fmt.Printf("no target specified, please use --target\n")
		os.Exit(1)
	}

	// create and populate the client configuration object
	config = modbus.Config{
		URL:      target,
		Speed:    speed,
		DataBits: dataBits,
		StopBits: stopBits,
	}

	switch parity {
	case "none":
		config.Parity = modbus.ParityNone
	case "odd":
		config.Parity = modbus.ParityOdd
	case "even":
		config.Parity = modbus.ParityEven
	default:
		fmt.Printf("unknown parity setting '%s' (should be one of none, odd or even)\n",
			parity)
		os.Exit(1)
	}

	config.Timeout, err = time.ParseDuration(timeout)
	if err != nil {
		fmt.Printf("failed to parse timeout setting '%s': %v\n", timeout, err)
		os.Exit(1)
	}

	cEndianness, err := parseEndianness(endianness)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	cWordOrder, err := parseWordOrder(wordOrder)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	// handle TLS options
	if strings.HasPrefix(target, "tcp+tls://") {
		if certPath == "" {
			fmt.Print("TLS requested but no client certificate given, please use --cert\n")
			os.Exit(1)
		}

		if keyPath == "" {
			fmt.Print("TLS requested but no client key given, please use --key\n")
			os.Exit(1)
		}

		if caPath == "" {
			fmt.Print("TLS requested but no CA/server cert given, please use --ca\n")
			os.Exit(1)
		}

		clientKeyPair, err = tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			fmt.Printf("failed to load client tls key pair: %v\n", err)
			os.Exit(1)
		}
		config.TLSClientCert = &clientKeyPair

		config.TLSRootCAs, err = modbus.LoadCertPool(caPath)
		if err != nil {
			fmt.Printf("failed to load tls CA/server certificate: %v\n", err)
			os.Exit(1)
		}
	}

	if len(flag.Args()) == 0 {
		fmt.Printf("nothing to do.\n")
		os.Exit(0)
	}

	runList, err := parseOperations(flag.Args())
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}

	// create the modbus client
	client, err = modbus.New(config)
	if err != nil {
		fmt.Printf("failed to create client: %v\n", err)
		os.Exit(1)
	}

	// set the initial unit id (note: this can be changed later at runtime through
	// the setUnitId command)
	if unitID > 0xff {
		fmt.Printf("set unit id: value '%v' out of range\n", unitID)
		os.Exit(1)
	}

	// connect to the remote host/open the serial port
	err = client.Open()
	if err != nil {
		fmt.Printf("failed to open client: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = client.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hadErrors := executeOperations(ctx, client, runList, uint8(unitID), cEndianness, cWordOrder, jsonOutput, failFast)
	if hadErrors {
		os.Exit(1)
	}
}
