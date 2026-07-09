package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage() string {
	return "usage: wormhole <command> [flags]\n\ncommands:\n  join    join a Wormhole project (RFC-0001 §8.5)"
}

// run dispatches to a subcommand and returns the process exit code. It
// takes explicit args/stdout/stderr so subcommands are testable without
// touching os.Args or os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage())
		return 2
	}
	switch args[0] {
	case "join":
		return runJoin(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "wormhole: unknown command %q\n\n%s\n", args[0], usage())
		return 2
	}
}

// runJoin parses and validates `wormhole join` flags. It is a scaffold
// only: it does not yet call the server. Passport issuance and the rest
// of the join flow (RFC-0001 §8.5) land Day 19+.
func runJoin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "Wormhole server base URL (required)")
	project := fs.String("project", "", "project ID to join (required)")
	owner := fs.String("owner", "", "human/org owner of this agent identity")
	model := fs.String("model", "", "model identifier for this agent identity")
	capabilities := fs.String("capabilities", "", "comma-separated list of agent capabilities")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *project == "" {
		fmt.Fprintln(stderr, "wormhole join: --server and --project are required")
		fs.Usage()
		return 2
	}

	var caps []string
	if *capabilities != "" {
		caps = strings.Split(*capabilities, ",")
	}

	fmt.Fprintf(stdout, "wormhole join: server=%s project=%s owner=%s model=%s capabilities=%v\n",
		*server, *project, *owner, *model, caps)
	fmt.Fprintln(stdout, "join flow not yet implemented: passport issuance and permission grant land Day 19 (RFC-0001 §8.5)")
	return 0
}
