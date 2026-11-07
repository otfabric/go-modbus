// SPDX-License-Identifier: MIT

package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagCompletions are the completions of the global flags with a value set
// or a file type; every other flag that takes a value gets no file names.
var flagCompletions = map[string]cobra.CompletionFunc{
	"target": cobra.FixedCompletions([]string{
		"tcp://\tModbus TCP (MBAP)",
		"tcp+tls://\tModbus TCP over TLS",
		"udp://\tModbus TCP over UDP",
		"rtu://\tModbus RTU (serial)",
		"rtuovertcp://\tRTU over TCP",
		"rtuoverudp://\tRTU over UDP",
		"ascii://\tModbus ASCII (serial)",
		"asciiovertcp://\tASCII over TCP",
	}, cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveNoSpace),
	"parity":     cobra.FixedCompletions([]string{"none", "even", "odd"}, cobra.ShellCompDirectiveNoFileComp),
	"data-bits":  cobra.FixedCompletions([]string{"0\tauto: 8 for rtu, 7 for ascii", "5", "6", "7", "8"}, cobra.ShellCompDirectiveNoFileComp),
	"stop-bits":  cobra.FixedCompletions([]string{"0\tauto", "1", "2"}, cobra.ShellCompDirectiveNoFileComp),
	"endianness": cobra.FixedCompletions([]string{"big", "little"}, cobra.ShellCompDirectiveNoFileComp),
	"word-order": cobra.FixedCompletions([]string{
		"highfirst\tmost significant word first",
		"hf\tmost significant word first (short)",
		"lowfirst\tleast significant word first",
		"lf\tleast significant word first (short)",
	}, cobra.ShellCompDirectiveNoFileComp),
	"cert": fileExt("pem", "crt", "cer"),
	"key":  fileExt("pem", "key"),
	"ca":   fileExt("pem", "crt", "cer"),
}

func fileExt(exts ...string) cobra.CompletionFunc {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return exts, cobra.ShellCompDirectiveFilterFileExt
	}
}

// registerCompletions gives every flag of every command that takes a value
// a completion function.
func registerCompletions(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		each := func(f *pflag.Flag) {
			if _, ok := c.GetFlagCompletionFunc(f.Name); ok || f.Value.Type() == "bool" {
				return
			}
			fn, ok := flagCompletions[f.Name]
			if !ok {
				fn = cobra.NoFileCompletions
			}
			_ = c.RegisterFlagCompletionFunc(f.Name, fn)
		}
		c.LocalFlags().VisitAll(each)
		c.PersistentFlags().VisitAll(each)
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)
}
