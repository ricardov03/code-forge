package main

import (
	"fmt"
	"os"
)

// Each block implementation registers its subcommands from its own file.
var blockSubs = map[string]func([]string) int{}

func init() { routes["block"] = runBlock }

func runBlock(args []string) int {
	if len(args) > 0 {
		if sub := blockSubs[args[0]]; sub != nil {
			return sub(args[1:])
		}
	}
	return blockHandover(args, hBlockUnportedSubcommand)
}

func blockHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"block"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}
