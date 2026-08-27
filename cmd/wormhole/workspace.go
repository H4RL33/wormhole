package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

type workspaceCommandBackend interface {
	Execute(context.Context, localapi.WorkspaceCommandRequest) (localapi.WorkspaceCommandResult, error)
}

type gatewayWorkspaceBackend struct {
	socketPath       string
	workingDirectory string
}

func (b *gatewayWorkspaceBackend) Execute(ctx context.Context, request localapi.WorkspaceCommandRequest) (localapi.WorkspaceCommandResult, error) {
	var result localapi.WorkspaceCommandResult
	err := callGatewayPrivateMethod(ctx, b.socketPath, localapi.PrivateWorkspaceRPCMethod, localapi.PrivateWorkspaceCommandRequest{
		WorkingDirectory: b.workingDirectory,
		Command:          request,
	}, &result)
	if err != nil {
		return localapi.WorkspaceCommandResult{}, err
	}
	if result.Operation != request.Operation {
		return localapi.WorkspaceCommandResult{}, fmt.Errorf("Gateway returned %q for %q", result.Operation, request.Operation)
	}
	return result, nil
}

var workspaceBackendFactory = func() (workspaceCommandBackend, error) {
	root, err := canonicalCurrentDirectory()
	if err != nil {
		return nil, err
	}
	return &gatewayWorkspaceBackend{socketPath: gatewaySocketPath(), workingDirectory: root}, nil
}

func runWorkspaceCommand(operation localapi.WorkspaceOperation, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(string(operation), flag.ContinueOnError)
	fs.SetOutput(stderr)
	var publicationReviewDigest, requestID, label string
	if operation == localapi.WorkspaceOperationCheckpoint {
		fs.StringVar(&publicationReviewDigest, "publication-review-digest", "", "acknowledge this public-Git publication review digest")
	}
	if operation == localapi.WorkspaceOperationStash {
		fs.StringVar(&requestID, "request-id", "", "unique stash request identifier")
		fs.StringVar(&label, "label", "", "human-readable stash label")
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "wormhole %s: unexpected arguments\n", operation)
		return 2
	}
	if operation == localapi.WorkspaceOperationStash && (requestID == "" || label == "") {
		fmt.Fprintln(stderr, "wormhole stash: --request-id and --label are required")
		return 2
	}
	backend, err := workspaceBackendFactory()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole %s: %v\n", operation, err)
		return 1
	}
	result, err := backend.Execute(context.Background(), localapi.WorkspaceCommandRequest{
		Operation: operation, PublicationReviewDigest: publicationReviewDigest, RequestID: requestID, Label: label,
	})
	if err != nil {
		fmt.Fprintf(stderr, "wormhole %s: %v\n", operation, err)
		return 1
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "wormhole %s: encode response: %v\n", operation, err)
		return 1
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}
