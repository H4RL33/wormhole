package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	projectconfig "github.com/H4RL33/wormhole/internal/config"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"golang.org/x/term"
)

type integrationCommandPlan struct {
	Operation      string                    `json:"operation"`
	ProjectID      string                    `json:"project_id"`
	ResolvedRole   string                    `json:"resolved_role"`
	ExpectedDigest string                    `json:"expected_digest"`
	Diff           string                    `json:"diff"`
	State          localapi.IntegrationState `json:"state"`
}

type integrationCommandBackend interface {
	Plan(context.Context, string, string) (integrationCommandPlan, error)
	Commit(context.Context, integrationCommandPlan) (localapi.IntegrationState, error)
}

type integrationGatewayCaller func(context.Context, string, string, any, any) error

type gatewayIntegrationBackend struct {
	socketPath     string
	repositoryRoot string
	projectID      string
	call           integrationGatewayCaller
}

func newGatewayIntegrationBackend() (integrationCommandBackend, error) {
	root, err := integrationRepositoryRoot()
	if err != nil {
		return nil, err
	}
	configured, err := projectconfig.LoadLocal()
	if err != nil {
		return nil, fmt.Errorf("load nearest project config: %w", err)
	}
	if strings.TrimSpace(configured.Project) == "" {
		return nil, errors.New("integration commands require a nearest .wormhole/config.toml project binding")
	}
	return &gatewayIntegrationBackend{socketPath: gatewaySocketPath(), repositoryRoot: root,
		projectID: strings.TrimSpace(configured.Project), call: callGatewayIntegrationMethod}, nil
}

func (backend *gatewayIntegrationBackend) Plan(ctx context.Context, operation, projectID string) (integrationCommandPlan, error) {
	if backend.projectID != "" && projectID != backend.projectID {
		return integrationCommandPlan{}, errors.New("requested project does not match nearest repository config")
	}
	request := localapi.IntegrationCommandRequest{Operation: operation, ProjectID: projectID, RepositoryRoot: backend.repositoryRoot}
	var response integrationCommandPlan
	if err := backend.call(ctx, backend.socketPath, "wormhole/integration/plan", request, &response); err != nil {
		return integrationCommandPlan{}, err
	}
	return response, nil
}

func (backend *gatewayIntegrationBackend) Commit(ctx context.Context, plan integrationCommandPlan) (localapi.IntegrationState, error) {
	request := localapi.IntegrationCommandRequest{
		Operation: plan.Operation, ProjectID: plan.ProjectID, RepositoryRoot: backend.repositoryRoot, ExpectedDigest: plan.ExpectedDigest,
	}
	var response localapi.IntegrationState
	if err := backend.call(ctx, backend.socketPath, "wormhole/integration/commit", request, &response); err != nil {
		return localapi.IntegrationState{}, err
	}
	return response, nil
}

var (
	integrationTerminal       = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	integrationBackendFactory = newGatewayIntegrationBackend
	integrationUUIDPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

func integrationRepositoryRoot() (string, error) {
	if configPath := projectconfig.LocalConfigPath(); configPath != "" {
		return filepath.Dir(filepath.Dir(configPath)), nil
	}
	return "", errors.New("integration commands require a nearest .wormhole/config.toml repository root")
}

func callGatewayIntegrationMethod(ctx context.Context, socketPath, method string, request, response any) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("gatewayd not running (dial %s: %w)", socketPath, err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	write := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = conn.Write(append(raw, '\n'))
		return err
	}
	read := func(destination any) error {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(line)))
		decoder.DisallowUnknownFields()
		return decoder.Decode(destination)
	}
	if err := write(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize", Params: json.RawMessage(`{}`)}); err != nil {
		return fmt.Errorf("write Gateway initialize request: %w", err)
	}
	var initialized rpcResponse
	if err := read(&initialized); err != nil {
		return fmt.Errorf("read Gateway initialize response: %w", err)
	}
	if initialized.Error != nil {
		return errors.New(initialized.Error.Message)
	}
	if err := write(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return fmt.Errorf("write Gateway initialized notification: %w", err)
	}
	params, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal integration command: %w", err)
	}
	if err := write(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: method, Params: params}); err != nil {
		return fmt.Errorf("write integration command: %w", err)
	}
	var commandResponse rpcResponse
	if err := read(&commandResponse); err != nil {
		return fmt.Errorf("read integration command response: %w", err)
	}
	if commandResponse.Error != nil {
		return errors.New(commandResponse.Error.Message)
	}
	decoder := json.NewDecoder(bytes.NewReader(commandResponse.Result))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(response); err != nil {
		return fmt.Errorf("decode integration command response: %w", err)
	}
	return nil
}

func runIntegrationCommand(args []string, stdout, stderr io.Writer) int {
	backend, err := integrationBackendFactory()
	if err != nil {
		fmt.Fprintf(stderr, "wormhole integration: %v\n", err)
		return 1
	}
	return runIntegration(args, os.Stdin, stdout, stderr, integrationTerminal(), backend)
}

func runIntegration(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool, backend integrationCommandBackend) int {
	if len(args) == 0 {
		integrationUsage(stderr)
		return 2
	}
	command := args[0]
	mutating := command == "apply" || command == "update" || command == "remove" || command == "rollback"
	if command != "preview" && command != "status" && !mutating {
		fmt.Fprintf(stderr, "unknown integration command %q\n", command)
		integrationUsage(stderr)
		return 2
	}

	flags := flag.NewFlagSet("integration "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	project := flags.String("project", "", "Wormhole project id (defaults to nearest project config)")
	jsonOutput := false
	if command == "status" {
		flags.BoolVar(&jsonOutput, "json", false, "print machine-readable status")
	}
	confirmDigest := ""
	if mutating {
		flags.StringVar(&confirmDigest, "confirm-digest", "", "confirm the full expected manifest digest")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "wormhole integration %s: unexpected operand %q\n", command, flags.Arg(0))
		return 2
	}
	if confirmDigest != "" && !integrationDigestValid(confirmDigest) {
		fmt.Fprintf(stderr, "wormhole integration %s: --confirm-digest requires a full lowercase sha256 digest\n", command)
		return 2
	}
	if mutating && !interactive && confirmDigest == "" {
		fmt.Fprintf(stderr, "wormhole integration %s: non-interactive mutation requires --confirm-digest\n", command)
		return 2
	}
	resolvedProject, err := resolveCodeGraphProject(*project)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole integration %s: %v\n", command, err)
		return 2
	}
	if !integrationUUIDPattern.MatchString(resolvedProject) || resolvedProject == "00000000-0000-0000-0000-000000000000" {
		fmt.Fprintf(stderr, "wormhole integration %s: project must be a canonical non-nil UUID\n", command)
		return 2
	}
	plan, err := backend.Plan(context.Background(), command, resolvedProject)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole integration %s: %v\n", command, err)
		return 1
	}
	if plan.ProjectID != resolvedProject {
		fmt.Fprintf(stderr, "wormhole integration %s: backend project binding mismatch\n", command)
		return 1
	}

	switch command {
	case "preview":
		writeIntegrationPlan(stdout, plan)
		return 0
	case "status":
		if jsonOutput {
			encoded, err := json.MarshalIndent(plan.State, "", "  ")
			if err != nil {
				fmt.Fprintf(stderr, "wormhole integration status: encode status: %v\n", err)
				return 1
			}
			fmt.Fprintln(stdout, string(encoded))
		} else {
			writeIntegrationStatus(stdout, plan.State)
		}
		return 0
	}

	if !integrationDigestValid(plan.ExpectedDigest) {
		fmt.Fprintf(stderr, "wormhole integration %s: backend returned an invalid expected digest\n", command)
		return 1
	}
	writeIntegrationPlan(stdout, plan)
	if confirmDigest != "" {
		if confirmDigest != plan.ExpectedDigest {
			fmt.Fprintf(stderr, "wormhole integration %s: --confirm-digest does not match expected digest\n", command)
			return 1
		}
	} else {
		fmt.Fprint(stdout, "Confirm? Type y, yes, or the full expected digest: ")
		answer, readErr := bufio.NewReader(stdin).ReadString('\n')
		if readErr != nil && answer == "" {
			fmt.Fprintf(stderr, "wormhole integration %s: read confirmation: %v\n", command, readErr)
			return 1
		}
		answer = strings.TrimSpace(answer)
		if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") && !strings.EqualFold(answer, plan.ExpectedDigest) {
			fmt.Fprintf(stderr, "wormhole integration %s: declined\n", command)
			return 1
		}
	}
	if _, err := backend.Commit(context.Background(), plan); err != nil {
		fmt.Fprintf(stderr, "wormhole integration %s: %v\n", command, err)
		return 1
	}
	fmt.Fprintf(stdout, "integration %s committed for project %s at %s\n", command, plan.ProjectID, plan.ExpectedDigest)
	return 0
}

func integrationDigestValid(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func writeIntegrationPlan(output io.Writer, plan integrationCommandPlan) {
	fmt.Fprintf(output, "operation=%s\nproject=%s\nrole=%s\ndigest=%s\n", plan.Operation, plan.ProjectID, plan.ResolvedRole, plan.ExpectedDigest)
	if plan.Diff != "" {
		fmt.Fprint(output, plan.Diff)
		if !strings.HasSuffix(plan.Diff, "\n") {
			fmt.Fprintln(output)
		}
	}
}

func writeIntegrationStatus(output io.Writer, state localapi.IntegrationState) {
	fmt.Fprintf(output, "project=%s\nresolved_role=%s\napproval_state=%s\nguidance_active=%t\nmaterialization_state=%s\nconnection_state=%s\ncompatibility_state=%s\ndrift_detected=%t\n",
		state.ProjectID, state.ResolvedRole, state.ApprovalState, state.GuidanceActive, state.MaterializationState, state.ConnectionState, state.CompatibilityState, state.DriftDetected)
	if state.ActiveManifestDigest != nil {
		fmt.Fprintf(output, "active_manifest_digest=%s\n", *state.ActiveManifestDigest)
	}
	if state.PendingManifestDigest != nil {
		fmt.Fprintf(output, "pending_manifest_digest=%s\n", *state.PendingManifestDigest)
	}
	if state.RollbackCandidateManifestVersion != nil {
		fmt.Fprintf(output, "rollback_candidate_manifest_version=%d\n", *state.RollbackCandidateManifestVersion)
	}
	if state.RollbackCandidateManifestDigest != nil {
		fmt.Fprintf(output, "rollback_candidate_manifest_digest=%s\n", *state.RollbackCandidateManifestDigest)
	}
	for _, target := range state.PreservedTargets {
		fmt.Fprintf(output, "preserved_target=%s\n", target)
	}
}

func integrationUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: wormhole integration <preview|apply|status|update|remove|rollback> [flags]")
}
