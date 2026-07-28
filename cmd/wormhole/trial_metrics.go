package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

const (
	trialMetricsKindParticipantPreview = "participant-preview"
	trialMetricsKindParticipant        = "participant"
	trialMetricsKindAggregate          = "aggregate"
)

func runTrialMetricsCommand(args []string, stdout, stderr io.Writer) int {
	return runTrialMetrics(args, os.Stdin, stdout, stderr)
}

func runTrialMetrics(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		trialMetricsUsage(stderr)
		return 2
	}
	action := args[0]
	if action != "validate" && action != "format" {
		fmt.Fprintf(stderr, "unknown trial-metrics command %q\n", action)
		trialMetricsUsage(stderr)
		return 2
	}

	flags := flag.NewFlagSet("trial-metrics "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		trialMetricsActionUsage(stderr, action)
		fmt.Fprintln(stderr, "\nflags:")
		flags.PrintDefaults()
	}
	kind := flags.String("kind", "", "input kind: participant-preview, participant, or aggregate")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !validTrialMetricsKind(*kind) {
		fmt.Fprintf(stderr, "wormhole trial-metrics %s: --kind must be participant-preview, participant, or aggregate\n", action)
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprintf(stderr, "wormhole trial-metrics %s: expected at most one FILE or - operand\n", action)
		return 2
	}
	path := "-"
	if flags.NArg() == 1 {
		path = flags.Arg(0)
	}

	data, err := readTrialMetricsInput(path, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "wormhole trial-metrics %s: %v\n", action, err)
		return 1
	}
	if action == "validate" {
		if err := validateTrialMetrics(*kind, data); err != nil {
			writeTrialMetricsValidationError(stderr, action, err)
			return 1
		}
		if _, err := fmt.Fprintln(stdout, "valid"); err != nil {
			fmt.Fprintf(stderr, "wormhole trial-metrics validate: write output: %v\n", err)
			return 1
		}
		return 0
	}
	formatted, err := formatTrialMetrics(*kind, data)
	if err != nil {
		writeTrialMetricsValidationError(stderr, action, err)
		return 1
	}
	if _, err := stdout.Write(formatted); err != nil {
		fmt.Fprintf(stderr, "wormhole trial-metrics format: write output: %v\n", err)
		return 1
	}
	return 0
}

func readTrialMetricsInput(path string, stdin io.Reader) ([]byte, error) {
	reader := stdin
	var file *os.File
	if path != "-" {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, localapi.TrialMetricsMaxJSONBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if len(data) > localapi.TrialMetricsMaxJSONBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", localapi.TrialMetricsMaxJSONBytes)
	}
	return data, nil
}

func validateTrialMetrics(kind string, data []byte) error {
	switch kind {
	case trialMetricsKindParticipantPreview:
		_, err := localapi.DecodeTrialParticipantPreview(data)
		return err
	case trialMetricsKindParticipant:
		_, err := localapi.DecodeTrialParticipantExport(data)
		return err
	case trialMetricsKindAggregate:
		_, err := localapi.DecodeTrialMetricsExport(data)
		return err
	default:
		return errors.New("unknown trial metrics kind")
	}
}

func formatTrialMetrics(kind string, data []byte) ([]byte, error) {
	switch kind {
	case trialMetricsKindParticipantPreview:
		export, err := localapi.DecodeTrialParticipantPreview(data)
		if err != nil {
			return nil, err
		}
		return localapi.MarshalTrialParticipantPreview(export)
	case trialMetricsKindParticipant:
		export, err := localapi.DecodeTrialParticipantExport(data)
		if err != nil {
			return nil, err
		}
		return localapi.MarshalTrialParticipantExport(export)
	case trialMetricsKindAggregate:
		export, err := localapi.DecodeTrialMetricsExport(data)
		if err != nil {
			return nil, err
		}
		return localapi.MarshalTrialMetricsExport(export)
	default:
		return nil, errors.New("unknown trial metrics kind")
	}
}

func writeTrialMetricsValidationError(stderr io.Writer, action string, err error) {
	message := "trial metrics validation failed"
	if errors.Is(err, localapi.ErrTrialPrivacy) {
		message = "trial metrics privacy violation"
	}
	fmt.Fprintf(stderr, "wormhole trial-metrics %s: %s\n", action, message)
}

func validTrialMetricsKind(kind string) bool {
	return kind == trialMetricsKindParticipantPreview || kind == trialMetricsKindParticipant || kind == trialMetricsKindAggregate
}

func trialMetricsActionUsage(output io.Writer, action string) {
	fmt.Fprintf(output, "usage: wormhole trial-metrics %s --kind <participant-preview|participant|aggregate> [FILE|-]\n", action)
}

func trialMetricsUsage(output io.Writer) {
	fmt.Fprintln(output, `usage: wormhole trial-metrics <command> [flags] [FILE|-]

commands:
  wormhole trial-metrics validate --kind <kind> [FILE|-]
  wormhole trial-metrics format --kind <kind> [FILE|-]

Input defaults to stdin. Validation and formatting are local and perform no network I/O.`)
}
