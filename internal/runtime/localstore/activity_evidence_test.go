package localstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestActivityNanosecondEvidenceReplaysAndRetainsExactly(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	createdAt := testUTCNow().Add(123_456_789 * time.Nanosecond)
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "nanosecond", createdAt))
	if err != nil {
		t.Fatal(err)
	}
	var storedCreated string
	if err := fixture.store.DB().QueryRow(`SELECT CAST(created_at AS TEXT) FROM activity_ledger WHERE activity_id=?`, record.Key.ActivityID).Scan(&storedCreated); err != nil {
		t.Fatal(err)
	}
	if storedCreated != "2026-08-28T12:00:00.123456789Z" {
		t.Fatalf("stored created_at=%q, want exact fixed-width UTC nanoseconds", storedCreated)
	}
	acceptedAt := testUTCNow().Add(987_654_321 * time.Nanosecond)
	receipt := localReceipt(t, record, 7, acceptedAt)
	if err := fixture.repo.AcknowledgeOutbound(context.Background(), record.Key, receipt); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.AcknowledgeOutbound(context.Background(), record.Key, receipt); err != nil {
		t.Fatalf("nanosecond acknowledge replay: %v", err)
	}
	retained, err := fixture.repo.Retained(context.Background(), fixture.route, 10)
	if err != nil || len(retained) != 1 || retained[0].Sequence == nil || *retained[0].Sequence != receipt.Sequence ||
		!retained[0].AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("retained acknowledged evidence=(%+v,%v), want sequence=%d accepted_at=%s", retained, err, receipt.Sequence, acceptedAt)
	}
	delivery := ActivityPullDelivery{
		SourceWorkspaceID: record.Key.SourceWorkspaceID,
		ActivityJSON:      append([]byte(nil), record.ActivityJSON...),
		ActivityDigest:    record.ActivityDigest,
	}
	delivery.ReceiptJSON, err = projectstate.CanonicalActivityReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	batch := localPullBatch(t, fixture.policy, 0, receipt.Sequence, false, delivery)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatalf("self-origin pull after acknowledgement: %v", err)
	}
	var cursorUpdatedAt string
	if err := fixture.store.DB().QueryRow(`SELECT updated_at FROM activity_cursors`).Scan(&cursorUpdatedAt); err != nil {
		t.Fatal(err)
	}
	before := activityTableCounts(t, fixture.store)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route, batch); err != nil {
		t.Fatalf("exact self-origin pull replay: %v", err)
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("self-origin replay mutated tables: before=%v after=%v", before, after)
	}
	var replayUpdatedAt string
	if err := fixture.store.DB().QueryRow(`SELECT updated_at FROM activity_cursors`).Scan(&replayUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if replayUpdatedAt != cursorUpdatedAt {
		t.Fatalf("self-origin replay changed cursor timestamp from %q to %q", cursorUpdatedAt, replayUpdatedAt)
	}
}

func TestActivityPrunerUsesNanosecondCreationOrder(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	source := localActivitySourceWorkspace
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	older := localPullDelivery(t, localOrdinaryActivity(localActivityIDTwo, "older", base.Add(100*time.Nanosecond)), source, 1, fixture.policy)
	newer := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "newer", base.Add(900*time.Nanosecond)), source, 2, fixture.policy)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route,
		localPullBatch(t, fixture.policy, 0, 2, false, older, newer)); err != nil {
		t.Fatal(err)
	}
	if pruned, err := fixture.repo.Prune(context.Background(), fixture.route, source, 1); err != nil || pruned != 1 {
		t.Fatalf("nanosecond prune=(%d,%v)", pruned, err)
	}
	retained, err := fixture.repo.Retained(context.Background(), fixture.route, 10)
	if err != nil || len(retained) != 1 || retained[0].Key.ActivityID != localActivityIDOne {
		t.Fatalf("nanosecond prune retained=(%+v,%v), want newer Activity", retained, err)
	}
}

func TestActivityPendingRejectsCorruptReferencedPolicyEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, localActivityFixture)
	}{
		{
			name: "canonical bytes",
			mutate: func(t *testing.T, fixture localActivityFixture) {
				t.Helper()
				if _, err := fixture.store.DB().Exec(`DROP TRIGGER activity_policy_versions_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.store.DB().Exec(`UPDATE activity_policy_versions SET canonical_policy_json=?`, []byte(`{"secret":"policy-evidence-must-not-leak"}`)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "captured digest",
			mutate: func(t *testing.T, fixture localActivityFixture) {
				t.Helper()
				conn, err := fixture.store.DB().Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				bad := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				if _, err := conn.ExecContext(context.Background(), `UPDATE activity_outbound_queue SET expected_policy_digest=?`, bad); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLocalActivityFixture(t, true)
			defer fixture.store.Close()
			if _, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
				localOrdinaryActivity(localActivityIDOne, "policy-corruption", testUTCNow())); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture)
			_, err := fixture.repo.PendingOutbound(context.Background(), fixture.route, 10)
			if err == nil {
				t.Fatal("PendingOutbound served corrupt policy evidence")
			}
			if strings.Contains(err.Error(), "policy-evidence-must-not-leak") {
				t.Fatalf("policy corruption error exposed canonical bytes: %v", err)
			}
		})
	}
}

func TestActivityRetainedRejectsCorruptLedgerAndReceiptEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, localActivityFixture)
	}{
		{
			name: "ledger creation time",
			mutate: func(t *testing.T, fixture localActivityFixture) {
				t.Helper()
				if _, err := fixture.store.DB().Exec(`DROP TRIGGER activity_ledger_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.store.DB().Exec(`UPDATE activity_ledger SET created_at='2001-01-01T00:00:00.000000000Z'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "receipt accepted time",
			mutate: func(t *testing.T, fixture localActivityFixture) {
				t.Helper()
				if _, err := fixture.store.DB().Exec(`DROP TRIGGER activity_ingress_receipts_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.store.DB().Exec(`UPDATE activity_ingress_receipts SET accepted_at='2001-01-01T00:00:00.000000000Z'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ledger sequence",
			mutate: func(t *testing.T, fixture localActivityFixture) {
				t.Helper()
				if _, err := fixture.store.DB().Exec(`DROP TRIGGER activity_ledger_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.store.DB().Exec(`UPDATE activity_ledger SET sequence=2`); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLocalActivityFixture(t, true)
			defer fixture.store.Close()
			delivery := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "retained-corruption",
				time.Date(2000, 1, 1, 0, 0, 0, 123, time.UTC)), localActivitySourceWorkspace, 1, fixture.policy)
			if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route,
				localPullBatch(t, fixture.policy, 0, 1, false, delivery)); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture)
			if _, err := fixture.repo.Retained(context.Background(), fixture.route, 10); err == nil {
				t.Fatal("Retained served corrupt evidence")
			}
		})
	}
}

func TestActivityTransitionRejectsCorruptLifecycleEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, localActivityFixture, ActivityRecord)
	}{
		{
			name: "terminal expiry",
			mutate: func(t *testing.T, fixture localActivityFixture, record ActivityRecord) {
				t.Helper()
				if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET expires_at='2001-01-01T00:00:00.000000000Z'
					WHERE activity_id=? AND lifecycle_kind='delivery'`, record.Key.ActivityID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "captured retention",
			mutate: func(t *testing.T, fixture localActivityFixture, record ActivityRecord) {
				t.Helper()
				if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET terminal_retention_seconds=3000000
					WHERE activity_id=? AND lifecycle_kind='delivery'`, record.Key.ActivityID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "captured policy digest",
			mutate: func(t *testing.T, fixture localActivityFixture, record ActivityRecord) {
				t.Helper()
				conn, err := fixture.store.DB().Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				bad := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				if _, err := conn.ExecContext(context.Background(), `UPDATE activity_lifecycle SET policy_digest=?
					WHERE activity_id=? AND lifecycle_kind='delivery'`, bad, record.Key.ActivityID); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLocalActivityFixture(t, true)
			defer fixture.store.Close()
			record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
				localOrdinaryActivity(localActivityIDOne, "lifecycle-corruption", testUTCNow()))
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.repo.AcknowledgeOutbound(context.Background(), record.Key,
				localReceipt(t, record, 1, testUTCNow().Add(time.Second))); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture, record)
			change := ActivityLifecycleChange{Kind: "delivery", ReferenceID: record.Key.ActivityID, ExpectedState: "pending", NextState: "delivered"}
			if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key, change); err == nil {
				t.Fatal("TransitionLifecycle replay accepted corrupt lifecycle evidence")
			}
		})
	}
}

func TestActivityPruneRejectsInvalidMaximumExpirySibling(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	reference := "c0000000-0000-4000-8000-000000000001"
	record, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localLifecycleActivity(localActivityIDOne, projectstate.ActivityLifecycleConflictV1, reference,
			time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.AcknowledgeOutbound(context.Background(), record.Key,
		localReceipt(t, record, 1, testUTCNow())); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.TransitionLifecycle(context.Background(), record.Key,
		ActivityLifecycleChange{Kind: "conflict", ReferenceID: reference, ExpectedState: "open", NextState: "resolved"}); err != nil {
		t.Fatal(err)
	}
	terminal := "2000-01-01T00:00:00.000000000Z"
	expires := "2000-01-31T00:00:00.000000000Z"
	if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET terminal_at=?,expires_at=? WHERE activity_id=?`, terminal, expires, record.Key.ActivityID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE activity_lifecycle SET expires_at='not-a-time'
		WHERE activity_id=? AND lifecycle_kind='delivery'`, record.Key.ActivityID); err != nil {
		t.Fatal(err)
	}
	before := activityTableCounts(t, fixture.store)
	if _, err := fixture.repo.Prune(context.Background(), fixture.route, record.Key.SourceWorkspaceID, 10); err == nil {
		t.Fatal("Prune accepted an invalid maximum-expiry sibling")
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed lifecycle evidence prune changed tables: before=%v after=%v", before, after)
	}
}

func TestActivityPruneCorruptLaterCandidateRollsBackCompletePreimage(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	first := localPullDelivery(t, localOrdinaryActivity(localActivityIDOne, "first", old), localActivitySourceWorkspace, 1, fixture.policy)
	second := localPullDelivery(t, localOrdinaryActivity(localActivityIDTwo, "second", old.Add(time.Second)), localActivitySourceWorkspace, 2, fixture.policy)
	if err := fixture.repo.AcceptPullBatch(context.Background(), fixture.route,
		localPullBatch(t, fixture.policy, 0, 2, false, first, second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`DROP TRIGGER activity_ledger_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE activity_ledger SET created_at='2000-01-01T00:00:02.000000000Z' WHERE activity_id=?`, localActivityIDTwo); err != nil {
		t.Fatal(err)
	}
	before := activityTableCounts(t, fixture.store)
	if _, err := fixture.repo.Prune(context.Background(), fixture.route, localActivitySourceWorkspace, 10); err == nil {
		t.Fatal("Prune accepted corrupt later candidate")
	}
	if after := activityTableCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed prune changed complete preimage: before=%v after=%v", before, after)
	}
}

func TestActivityEvidenceErrorsRemainOpaque(t *testing.T) {
	fixture := newLocalActivityFixture(t, true)
	defer fixture.store.Close()
	if _, err := fixture.repo.QueueOutbound(context.Background(), fixture.route,
		localOrdinaryActivity(localActivityIDOne, "opaque-evidence", testUTCNow())); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`DROP TRIGGER activity_policy_versions_no_update`); err != nil {
		t.Fatal(err)
	}
	secret := "credential-ref:secret-policy-evidence"
	if _, err := fixture.store.DB().Exec(`UPDATE activity_policy_versions SET canonical_policy_json=?`, []byte(secret)); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.repo.PendingOutbound(context.Background(), fixture.route, 1)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), fixture.route.CanonicalRef) {
		t.Fatalf("opaque evidence error=%v", err)
	}
	if !errors.Is(err, ErrActivityPolicyUnavailable) && !errors.Is(err, ErrActivityReplayConflict) {
		t.Fatalf("opaque evidence error lost stable sentinel: %v", err)
	}
}
