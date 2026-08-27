package connector

import (
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

func TestConnectorEntryCanonicalValidationAndDigest(t *testing.T) {
	entry := ConnectorEntry{
		State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio,
		Command: "/opt/wormhole", Args: []string{"mcp"},
		Env: []EnvironmentVariable{{Name: "ALPHA", Value: "one"}, {Name: "ZETA", Value: "two"}},
	}
	digest, err := DigestConnectorEntry(entry)
	if err != nil {
		t.Fatalf("DigestConnectorEntry: %v", err)
	}
	if _, err := config.ParseStateDigest(string(digest)); err != nil {
		t.Fatalf("digest is not canonical: %v", err)
	}
	clone := entry
	clone.Env = []EnvironmentVariable{{Name: "ZETA", Value: "two"}, {Name: "ALPHA", Value: "one"}}
	if _, err := DigestConnectorEntry(clone); !errors.Is(err, ErrInvalidConnectorEntry) {
		t.Fatalf("unsorted environment error = %v", err)
	}
	for name, candidate := range map[string]ConnectorEntry{
		"absent payload":  {State: EntryAbsent, Scope: ScopeUser},
		"unknown state":   {State: "unknown"},
		"wrong scope":     {State: EntryPresent, Scope: "project", Transport: TransportStdio, Command: "wormhole", Args: []string{}, Env: []EnvironmentVariable{}},
		"wrong transport": {State: EntryPresent, Scope: ScopeUser, Transport: "http", Command: "wormhole", Args: []string{}, Env: []EnvironmentVariable{}},
		"duplicate env":   {State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "wormhole", Args: []string{}, Env: []EnvironmentVariable{{Name: "A", Value: "1"}, {Name: "A", Value: "2"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DigestConnectorEntry(candidate); !errors.Is(err, ErrInvalidConnectorEntry) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestConnectorEntryAcceptsNativeLowercaseEnvironmentNames(t *testing.T) {
	entry := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/prior/server", Args: []string{}, Env: []EnvironmentVariable{{Name: "lower_name", Value: "value"}}}
	if _, err := DigestConnectorEntry(entry); err != nil {
		t.Fatalf("lowercase environment name: %v", err)
	}
}

func TestConfirmedConnectorChangeValidation(t *testing.T) {
	prior := ConnectorEntry{State: EntryAbsent}
	desired := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	plan, err := BuildChangePlan(AdapterCodex, "wormhole", OperationInstall, prior, desired)
	if err != nil {
		t.Fatal(err)
	}
	priorDigest, _ := DigestConnectorEntry(prior)
	desiredDigest, _ := DigestConnectorEntry(desired)
	change := ConfirmedConnectorChange{Adapter: AdapterCodex, Name: "wormhole", Action: OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
	if err := ValidateConfirmedConnectorChange(change); err != nil {
		t.Fatalf("valid change: %v", err)
	}
	change.Name = "../secret"
	if err := ValidateConfirmedConnectorChange(change); !errors.Is(err, config.ErrConfirmedPlanDrift) {
		t.Fatalf("unsafe name error = %v", err)
	}
	for _, name := range []string{"--help", ".", "-wormhole"} {
		change.Name = name
		if err := ValidateConfirmedConnectorChange(change); !errors.Is(err, config.ErrConfirmedPlanDrift) {
			t.Fatalf("option-shaped name %q error = %v", name, err)
		}
	}
}
