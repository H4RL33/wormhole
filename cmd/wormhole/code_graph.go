package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	projectconfig "github.com/H4RL33/wormhole/internal/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"golang.org/x/term"
)

type codeGraphCommandRunner func(context.Context, localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error)

var codeGraphTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "code-graph" {
		fmt.Fprintln(stderr, "usage: wormhole config code-graph <enable|disable|status|rebuild|checkout>")
		return 2
	}
	return runConfigCodeGraph(args[1:], os.Stdin, stdout, stderr, codeGraphTerminal(), executeCodeGraphLifecycle)
}

func runConfigCodeGraph(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool, call codeGraphCommandRunner) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: wormhole config code-graph <enable|disable|status|rebuild|checkout>")
		return 2
	}
	operation := localapi.CodeGraphLifecycleOperation("")
	command := args[0]
	leafArgs := args[1:]
	checkout := ""
	switch command {
	case "enable":
		operation = localapi.CodeGraphEnable
	case "disable":
		operation = localapi.CodeGraphDisable
	case "status":
		operation = localapi.CodeGraphStatus
	case "rebuild":
		operation = localapi.CodeGraphRebuild
	case "checkout":
		if len(leafArgs) == 0 {
			fmt.Fprintln(stderr, "usage: wormhole config code-graph checkout <set|show>")
			return 2
		}
		subcommand := leafArgs[0]
		leafArgs = leafArgs[1:]
		switch subcommand {
		case "set":
			operation = localapi.CodeGraphCheckoutSet
		case "show":
			operation = localapi.CodeGraphCheckoutShow
		default:
			fmt.Fprintf(stderr, "unknown code-graph checkout command %q\n", subcommand)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "unknown code-graph command %q\n", command)
		return 2
	}

	name := "config code-graph " + strings.ReplaceAll(string(operation), "_", " ")
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "Wormhole project id (defaults to nearest project config)")
	mutating := operation != localapi.CodeGraphStatus && operation != localapi.CodeGraphCheckoutShow
	confirm := false
	if mutating {
		fs.BoolVar(&confirm, "confirm", false, "explicitly confirm this human lifecycle mutation")
	}
	if err := fs.Parse(leafArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	operands := fs.Args()
	if operation == localapi.CodeGraphCheckoutSet {
		if len(operands) != 1 {
			fmt.Fprintln(stderr, "usage: wormhole config code-graph checkout set [--project PROJECT] [--confirm] PATH")
			return 2
		}
		checkout = operands[0]
	} else if len(operands) != 0 {
		fmt.Fprintf(stderr, "wormhole %s: unexpected operand %q\n", name, operands[0])
		return 2
	}
	if operation == localapi.CodeGraphEnable {
		current, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "wormhole config code-graph enable: %v\n", err)
			return 1
		}
		checkout = current
	}
	resolvedProject, err := resolveCodeGraphProject(*project)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole %s: %v\n", name, err)
		return 2
	}
	if mutating {
		if operation == localapi.CodeGraphEnable {
			fmt.Fprintln(stdout, "Code Graph is local and experimental. Building it uses CPU, memory, disk space, and disk I/O.")
		}
		if operation == localapi.CodeGraphDisable {
			fmt.Fprintln(stdout, "This will delete all completed revisions, candidate revisions, nodes, files, symbols, edges, diagnostics, and project Code Graph configuration. Git and the working tree will be left unchanged.")
		}
		if !confirm {
			if !interactive {
				fmt.Fprintf(stderr, "wormhole %s: non-interactive mutation requires --confirm\n", name)
				return 2
			}
			fmt.Fprint(stdout, "Continue? Type yes to confirm: ")
			answer, readErr := bufio.NewReader(stdin).ReadString('\n')
			if readErr != nil && len(answer) == 0 {
				fmt.Fprintf(stderr, "wormhole %s: read confirmation: %v\n", name, readErr)
				return 1
			}
			if strings.TrimSpace(answer) != "yes" {
				fmt.Fprintln(stderr, "Code Graph lifecycle change declined")
				return 1
			}
		}
	}
	status, err := call(context.Background(), localapi.CodeGraphLifecycleRequest{Operation: operation, ProjectID: resolvedProject, Checkout: checkout})
	if err != nil {
		fmt.Fprintf(stderr, "wormhole %s: %v\n", name, err)
		return 1
	}
	if operation == localapi.CodeGraphDisable {
		fmt.Fprintf(stdout, "project=%s enabled=false\n", resolvedProject)
		return 0
	}
	if operation == localapi.CodeGraphCheckoutShow {
		fmt.Fprintln(stdout, status.ActiveCheckout)
		return 0
	}
	fmt.Fprintf(stdout, "project=%s enabled=%t checkout=%s remote=%s revision=%s commit=%s\n", status.ProjectID, status.Enabled, status.ActiveCheckout, status.CanonicalRemote, status.ActiveRevision, status.IndexedCommit)
	return 0
}

func resolveCodeGraphProject(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	configured, err := projectconfig.LoadLocal()
	if err != nil {
		return "", fmt.Errorf("load nearest project config: %w", err)
	}
	if strings.TrimSpace(configured.Project) == "" {
		return "", errors.New("--project is required when no nearest .wormhole/config.toml project is configured")
	}
	return strings.TrimSpace(configured.Project), nil
}

func executeCodeGraphLifecycle(ctx context.Context, request localapi.CodeGraphLifecycleRequest) (localapi.CodeGraphLifecycleStatus, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return localapi.CodeGraphLifecycleStatus{}, fmt.Errorf("observe working directory: %w", err)
	}
	workingDirectory, err = filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		return localapi.CodeGraphLifecycleStatus{}, fmt.Errorf("canonicalize working directory: %w", err)
	}
	privateRequest := struct {
		localapi.CodeGraphLifecycleRequest
		Workspace localapi.PrivateRequestContext `json:"_wormhole_workspace"`
	}{CodeGraphLifecycleRequest: request, Workspace: localapi.PrivateRequestContext{WorkingDirectory: filepath.Clean(workingDirectory)}}
	var response localapi.CodeGraphLifecycleStatus
	if err := callGatewayPrivateMethod(ctx, gatewaySocketPath(), codeGraphLifecycleRPCMethod, privateRequest, &response); err != nil {
		return localapi.CodeGraphLifecycleStatus{}, err
	}
	return response, nil
}
