// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/otfabric/go-modbus"
	"github.com/spf13/cobra"
)

// options holds the values of the global flags.
type options struct {
	target     string
	speed      uint
	dataBits   uint
	parity     string
	stopBits   uint
	timeout    string
	unitID     uint
	certPath   string
	keyPath    string
	caPath     string
	endianness string
	wordOrder  string
	json       bool
	failFast   bool
}

// newRootCmd builds the command tree with a fresh set of flags.
func newRootCmd() *cobra.Command {
	opts := &options{}

	rootCmd := &cobra.Command{
		Use:   "modbus-cli [flags] operation [operation...]",
		Short: "Modbus command line interface client for probing and troubleshooting",
		Long: `A modbus command line interface client for quick interaction with modbus
devices (e.g. for probing or troubleshooting).

Operations are given as trailing arguments using a colon-separated DSL.

` + operationsHelp,
		Example: operationsExamples,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoot(cmd, opts, args)
		},
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: completeOperations,
	}

	f := rootCmd.PersistentFlags()
	f.StringVar(&opts.target, "target", "", "target device to connect to (e.g. tcp://somehost:502)")
	f.UintVar(&opts.speed, "speed", 19200, "serial bus speed in bps (rtu, ascii)")
	f.UintVar(&opts.dataBits, "data-bits", 0, "data bits per character: 0 (auto: 8 for rtu, 7 for ascii), 5-8")
	f.StringVar(&opts.parity, "parity", "none", "parity bit: none, even, odd (rtu, ascii)")
	f.UintVar(&opts.stopBits, "stop-bits", 0, "stop bits: 0 (auto), 1, 2 (rtu, ascii)")
	f.StringVar(&opts.timeout, "timeout", "3s", "request timeout (e.g. 3s, 500ms)")
	f.StringVar(&opts.endianness, "endianness", "big", "register endianness: big, little")
	f.StringVar(&opts.wordOrder, "word-order", "highfirst", "word order: highfirst|hf, lowfirst|lf")
	f.UintVar(&opts.unitID, "unit-id", 1, "unit/slave ID (0-255)")
	f.StringVar(&opts.certPath, "cert", "", "TLS client certificate path")
	f.StringVar(&opts.keyPath, "key", "", "TLS client key path")
	f.StringVar(&opts.caPath, "ca", "", "TLS CA/server certificate path")
	f.BoolVar(&opts.json, "json", false, "output as line-delimited JSON")
	f.BoolVar(&opts.failFast, "fail-fast", false, "stop on first operation error")

	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err) })
	rootCmd.AddCommand(newVersionCmd(opts))
	registerCompletions(rootCmd)

	return rootCmd
}

// Run executes the command line args and returns the process exit code (one
// of the Exit… constants). ctx is the root context: cancelling it interrupts
// the run, which then exits with ExitInterrupted.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	rootCmd := newRootCmd()
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)

	err := rootCmd.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
	if ctx.Err() != nil {
		return ExitInterrupted
	}
	return exitCode(err)
}

func completeOperations(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{
		"rc:\tread coils",
		"rdi:\tread discrete inputs",
		"rh:\tread holding registers",
		"ri:\tread input registers",
		"wc:\twrite coil",
		"wr:\twrite register",
		"scan:\tscan address space",
		"ping:\tping device",
		"sleep:\tpause execution",
		"suid:\tset unit ID",
		"sid:\tset unit ID (short)",
		"repeat\trestart all operations",
		"date\tprint current date/time",
	}, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

func runRoot(cmd *cobra.Command, opts *options, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}

	if opts.target == "" {
		return usagef("no target specified, use --target")
	}

	config := modbus.Config{
		URL:      opts.target,
		Speed:    opts.speed,
		DataBits: opts.dataBits,
		StopBits: opts.stopBits,
	}

	switch opts.parity {
	case "none":
		config.Parity = modbus.ParityNone
	case "odd":
		config.Parity = modbus.ParityOdd
	case "even":
		config.Parity = modbus.ParityEven
	default:
		return usagef("unknown parity setting %q (should be none, even, or odd)", opts.parity)
	}

	switch opts.dataBits {
	case 0, 5, 6, 7, 8:
	default:
		return usagef("data bits %d out of range (0 for auto, or 5-8)", opts.dataBits)
	}

	switch opts.stopBits {
	case 0, 1, 2:
	default:
		return usagef("stop bits %d out of range (0 for auto, 1 or 2)", opts.stopBits)
	}

	var err error
	config.Timeout, err = time.ParseDuration(opts.timeout)
	if err != nil {
		return usagef("failed to parse timeout %q: %w", opts.timeout, err)
	}
	// the library replaces a zero timeout with its default and a negative
	// one fails every request: neither is what was asked for
	if config.Timeout <= 0 {
		return usagef("timeout %q must be greater than 0", opts.timeout)
	}

	if opts.unitID > 0xff {
		return usagef("unit ID %d out of range (0-255)", opts.unitID)
	}
	unitID := uint8(opts.unitID)

	cEndianness, err := parseEndianness(opts.endianness)
	if err != nil {
		return usageError(err)
	}
	cWordOrder, err := parseWordOrder(opts.wordOrder)
	if err != nil {
		return usageError(err)
	}

	runList, err := parseOperations(args)
	if err != nil {
		return usageError(err)
	}

	if strings.HasPrefix(opts.target, "tcp+tls://") {
		if opts.certPath == "" {
			return usagef("TLS requested but no client certificate given, use --cert")
		}
		if opts.keyPath == "" {
			return usagef("TLS requested but no client key given, use --key")
		}
		if opts.caPath == "" {
			return usagef("TLS requested but no CA/server cert given, use --ca")
		}

		clientKeyPair, tlsErr := tls.LoadX509KeyPair(opts.certPath, opts.keyPath)
		if tlsErr != nil {
			return inputError(fmt.Errorf("failed to load client TLS key pair: %w", tlsErr))
		}
		config.TLSClientCert = &clientKeyPair

		config.TLSRootCAs, err = modbus.LoadCertPool(opts.caPath)
		if err != nil {
			return inputError(fmt.Errorf("failed to load TLS CA/server certificate: %w", err))
		}
	}

	client, err := modbus.New(config)
	if err != nil {
		return usageError(fmt.Errorf("failed to create client: %w", err))
	}

	if err := client.Open(); err != nil {
		return &transportError{fmt.Errorf("failed to open client: %w", err)}
	}
	defer func() { _ = client.Close() }()

	ctx := cmd.Context()
	out := newOutput(cmd.OutOrStdout(), opts.json)
	opErr := executeOperations(ctx, client, runList, unitID, cEndianness, cWordOrder, out, opts.failFast)
	switch {
	case ctx.Err() != nil:
		return errInterrupted
	case out.err != nil:
		return outputError(fmt.Errorf("failed to write results: %w", out.err))
	}
	return opErr
}
