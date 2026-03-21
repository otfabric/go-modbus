package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/otfabric/go-modbus"
	"github.com/spf13/cobra"
)

var errOperationFailed = errors.New("operation failed")

var (
	flagTarget     string
	flagSpeed      uint
	flagDataBits   uint
	flagParity     string
	flagStopBits   uint
	flagTimeout    string
	flagUnitID     uint
	flagCertPath   string
	flagKeyPath    string
	flagCAPath     string
	flagEndianness string
	flagWordOrder  string
	flagJSON       bool
	flagFailFast   bool
)

var rootCmd = &cobra.Command{
	Use:   "modbus-cli [flags] operation [operation...]",
	Short: "Modbus command line interface client for probing and troubleshooting",
	Long: `A modbus command line interface client for quick interaction with modbus
devices (e.g. for probing or troubleshooting).

Operations are given as trailing arguments using a colon-separated DSL.

` + operationsHelp,
	Example:       operationsExamples,
	Args:          cobra.ArbitraryArgs,
	RunE:          runRoot,
	SilenceUsage:  true,
	SilenceErrors: true,
	ValidArgsFunction: completeOperations,
}

func init() {
	f := rootCmd.PersistentFlags()
	f.StringVar(&flagTarget, "target", "", "target device to connect to (e.g. tcp://somehost:502)")
	f.UintVar(&flagSpeed, "speed", 19200, "serial bus speed in bps (rtu)")
	f.UintVar(&flagDataBits, "data-bits", 8, "number of data bits per character (rtu)")
	f.StringVar(&flagParity, "parity", "none", "parity bit: none, even, odd (rtu)")
	f.UintVar(&flagStopBits, "stop-bits", 0, "stop bits: 0 (auto), 1, 2 (rtu)")
	f.StringVar(&flagTimeout, "timeout", "3s", "request timeout (e.g. 3s, 500ms)")
	f.StringVar(&flagEndianness, "endianness", "big", "register endianness: big, little")
	f.StringVar(&flagWordOrder, "word-order", "highfirst", "word order: highfirst|hf, lowfirst|lf")
	f.UintVar(&flagUnitID, "unit-id", 1, "unit/slave ID (0-255)")
	f.StringVar(&flagCertPath, "cert", "", "TLS client certificate path")
	f.StringVar(&flagKeyPath, "key", "", "TLS client key path")
	f.StringVar(&flagCAPath, "ca", "", "TLS CA/server certificate path")
	f.BoolVar(&flagJSON, "json", false, "output as line-delimited JSON")
	f.BoolVar(&flagFailFast, "fail-fast", false, "stop on first operation error")

	_ = rootCmd.RegisterFlagCompletionFunc("parity", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"none", "even", "odd"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = rootCmd.RegisterFlagCompletionFunc("endianness", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"big", "little"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = rootCmd.RegisterFlagCompletionFunc("word-order", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"highfirst\tmost significant word first",
			"hf\tmost significant word first (short)",
			"lowfirst\tleast significant word first",
			"lf\tleast significant word first (short)",
		}, cobra.ShellCompDirectiveNoFileComp
	})

	rootCmd.Version = getVersion()
	rootCmd.SetVersionTemplate("modbus-cli v{{.Version}}\n")
	rootCmd.AddCommand(versionCmd)
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

func runRoot(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}

	if flagTarget == "" {
		return fmt.Errorf("no target specified, use --target")
	}

	config := modbus.Config{
		URL:      flagTarget,
		Speed:    flagSpeed,
		DataBits: flagDataBits,
		StopBits: flagStopBits,
	}

	switch flagParity {
	case "none":
		config.Parity = modbus.ParityNone
	case "odd":
		config.Parity = modbus.ParityOdd
	case "even":
		config.Parity = modbus.ParityEven
	default:
		return fmt.Errorf("unknown parity setting '%s' (should be none, even, or odd)", flagParity)
	}

	var err error
	config.Timeout, err = time.ParseDuration(flagTimeout)
	if err != nil {
		return fmt.Errorf("failed to parse timeout '%s': %w", flagTimeout, err)
	}

	cEndianness, err := parseEndianness(flagEndianness)
	if err != nil {
		return err
	}
	cWordOrder, err := parseWordOrder(flagWordOrder)
	if err != nil {
		return err
	}

	if strings.HasPrefix(flagTarget, "tcp+tls://") {
		if flagCertPath == "" {
			return fmt.Errorf("TLS requested but no client certificate given, use --cert")
		}
		if flagKeyPath == "" {
			return fmt.Errorf("TLS requested but no client key given, use --key")
		}
		if flagCAPath == "" {
			return fmt.Errorf("TLS requested but no CA/server cert given, use --ca")
		}

		clientKeyPair, tlsErr := tls.LoadX509KeyPair(flagCertPath, flagKeyPath)
		if tlsErr != nil {
			return fmt.Errorf("failed to load client TLS key pair: %w", tlsErr)
		}
		config.TLSClientCert = &clientKeyPair

		config.TLSRootCAs, err = modbus.LoadCertPool(flagCAPath)
		if err != nil {
			return fmt.Errorf("failed to load TLS CA/server certificate: %w", err)
		}
	}

	runList, err := parseOperations(args)
	if err != nil {
		return err
	}

	client, err := modbus.New(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	if flagUnitID > 0xff {
		return fmt.Errorf("unit ID %d out of range (0-255)", flagUnitID)
	}

	if err := client.Open(); err != nil {
		return fmt.Errorf("failed to open client: %w", err)
	}
	defer func() { _ = client.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if executeOperations(ctx, client, runList, uint8(flagUnitID), cEndianness, cWordOrder, flagJSON, flagFailFast) {
		return errOperationFailed
	}
	return nil
}
