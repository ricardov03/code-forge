package main

import (
	"fmt"
	"os"
)

// Each block implementation registers its subcommands from its own file.
var blockSubs = map[string]func([]string) int{}

func init() { routes["block"] = runBlock }

func runBlock(args []string) int {
	// B648: `block --help` and `block -h` print Node's block USAGE in Go.
	if len(args) > 0 && metaIsHelp(args[0]) {
		fmt.Fprint(os.Stdout, blockUsage)
		return 0
	}
	if len(args) > 0 {
		if sub := blockSubs[args[0]]; sub != nil {
			return sub(args[1:])
		}
	}
	return blockHandover(args, hBlockUnportedSubcommand)
}

// blockUsage is USAGE in src/cli/block.mjs, byte for byte.
const blockUsage = "usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract|light] [--tests <paths…>] [--after <ids…>] [--after-outcome <ids…>]\n" +
	"       code-forge block ready <id> --run <r> [--transcript <file>] [--report <file>]\n" +
	"       code-forge block rebase <id> --run <r> --accept-landed\n" +
	"       code-forge block attempt|rebase <id> --run <r> · block close <id> --run <r> [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] · block claim <id> <path> --run <r> · block stop <id> --run <r> --reason <text>\n" +
	"       code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes]\n" +
	"       code-forge block reopen <id> --run <r> --file <path> --reason <text> [--yes]\n" +
	"       code-forge block accept-hit <id> --run <r> [--rule <rule-id>] --line <n> --transcript <path> --reason <text>\n" +
	"       code-forge block unwaive <id> <finding-id> --run <r> --file <path> --reason <text>\n" +
	"       code-forge block ack-waivers <id> --run <r>\n" +
	"       code-forge block human-pass <id> --run <r> --note <text> [--proof <dir-or-file>]\n" +
	"       code-forge block amend-acceptance <id> --run <r> --acceptance <file> --reason <text>\n" +
	"       code-forge block land <id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--push] [--watch-ci] [--copy-links]\n" +
	"       code-forge block land <id> <id>... --run <r> [--max <dur>] [--push] [--watch-ci] [--copy-links]\n" +
	"       every block verb takes --json: one JSON line on stdout ({\"block\",\"event\",…}) instead of the text\n"

func blockHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"block"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}
