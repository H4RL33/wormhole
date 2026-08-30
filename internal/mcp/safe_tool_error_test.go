package mcp

import (
	"strings"
	"testing"
)

func TestPublicToolFailureResultHasExactCanonicalSafeBytes(t *testing.T) {
	result, err := toolFailureResult("wormhole.sync.push", "authentication_failed")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"authentication_failed","operation":"wormhole.sync.push"}`
	if !result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != want {
		t.Fatalf("result = %+v, want exact %s", result, want)
	}
	for _, secret := range []string{"wrapped database cause", "/private/path", "Bearer token", "attachment-ref"} {
		if strings.Contains(result.Content[0].Text, secret) {
			t.Fatalf("safe result leaked %q", secret)
		}
	}
}

func TestPublicToolFailureResultRejectsUnknownCodesAndPrivateOperations(t *testing.T) {
	if _, err := toolFailureResult("wormhole.sync.push", "database_exploded"); err == nil {
		t.Fatal("unknown public failure code accepted")
	}
	if _, err := toolFailureResult("wormhole.task.create", "internal_error"); err == nil {
		t.Fatal("retained private operation accepted by public safe encoder")
	}
}

func TestPublicToolFailureCodeSetIsClosed(t *testing.T) {
	want := []string{
		"activity_cursor_invalid", "activity_lifecycle_conflict", "activity_not_found", "activity_policy_changed",
		"activity_policy_required", "activity_replay_conflict", "attachment_not_found", "authentication_failed",
		"internal_error", "invalid_activity", "invalid_request", "permission_denied", "sync_conflict",
		"sync_observer_unavailable", "sync_precondition_failed", "sync_replay_conflict", "unknown_activity_version", "unknown_version",
	}
	got := publicToolFailureCodes()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("codes = %q, want %q", got, want)
	}
}

func TestSafeToolErrorSyncV2PushCodesHaveExactCanonicalBytes(t *testing.T) {
	for _, code := range []string{
		"invalid_request",
		"authentication_failed",
		"attachment_not_found",
		"permission_denied",
		"sync_precondition_failed",
		"sync_conflict",
		"sync_replay_conflict",
		"internal_error",
	} {
		t.Run(code, func(t *testing.T) {
			result, err := toolFailureResult("wormhole.sync.push", code)
			if err != nil {
				t.Fatal(err)
			}
			want := `{"code":"` + code + `","operation":"wormhole.sync.push"}`
			if !result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != want {
				t.Fatalf("result = %+v, want exact %s", result, want)
			}
		})
	}
}

func TestSafeToolErrorSyncV2ConflictCodesHaveExactCanonicalBytes(t *testing.T) {
	for _, code := range []string{
		"invalid_request",
		"authentication_failed",
		"attachment_not_found",
		"permission_denied",
		"sync_precondition_failed",
		"sync_conflict",
		"sync_replay_conflict",
		"internal_error",
	} {
		t.Run(code, func(t *testing.T) {
			result, err := toolFailureResult("wormhole.sync.conflict", code)
			if err != nil {
				t.Fatal(err)
			}
			want := `{"code":"` + code + `","operation":"wormhole.sync.conflict"}`
			if !result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != want {
				t.Fatalf("result = %+v, want exact %s", result, want)
			}
		})
	}
}
