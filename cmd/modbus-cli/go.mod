module github.com/otfabric/go-modbus/cmd/modbus-cli

go 1.23

require (
	github.com/otfabric/go-modbus v1.9.0
	github.com/spf13/cobra v1.10.2
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/otfabric/go-serial v0.1.8 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)

// The CLI is built from the library in this repository. v1.9.0 still contains
// this package, so the requirement alone is an ambiguous import.
replace github.com/otfabric/go-modbus => ../..
