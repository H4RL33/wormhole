package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	exit := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(exit)
}

func run(args []string, stdout, stderr io.Writer) int {
	// Dispatch table: join, connect, whoami, profile, viewer-key, mcp
	if len(args) == 0 {
		usage(stderr)
		return 1
	}

	cmd := args[0]
	switch cmd {
	case "join":
		return runJoin(args[1:], stdout, stderr)
	case "connect":
		return runConnect(args[1:], stdout, stderr)
	case "whoami":
		return runWhoami(args[1:], stdout, stderr)
	case "profile":
		return runProfile(args[1:], stdout, stderr)
	case "viewer-key":
		return runViewerKey(args[1:], stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", cmd)
		usage(stderr)
		return 1
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `wormhole - agent memory portal

usage:
  wormhole join [flags]                  register this agent at a project
  wormhole connect [flags]               wire harnesses to credentials
  wormhole whoami [flags]                show this agent's identity
  wormhole profile list [flags]          list stored credential profiles
  wormhole viewer-key create [flags]     issue a viewer passport
  wormhole mcp                           stdio↔socket bridge for MCP harness (no flags)
  wormhole help                          show this message

`)
}

// Stub handlers (to be implemented in Task 2+)

func runJoin(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "join: not yet implemented\n")
	return 1
}

func runConnect(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "connect: not yet implemented\n")
	return 1
}

func runWhoami(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "whoami: not yet implemented\n")
	return 1
}

func runProfile(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "profile: not yet implemented\n")
	return 1
}

func runViewerKey(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "viewer-key: not yet implemented\n")
	return 1
}

func runMCP(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "mcp: not yet implemented\n")
	return 1
}
