package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const (
	activityTestID        = "99999999-9999-4999-8999-999999999991"
	activityReferenceID   = "99999999-9999-4999-8999-999999999992"
	activityAgentID       = "11111111-1111-4111-8111-111111111111"
	activityHumanID       = "22222222-2222-4222-8222-222222222222"
	activitySessionID     = "33333333-3333-4333-8333-333333333333"
	activityChannelID     = "44444444-4444-4444-8444-444444444444"
	activityTaskID        = "55555555-5555-4555-8555-555555555555"
	activityDigestGolden  = Digest("sha256:3465295f7f7c7a9671fd2ce27ada777c5a6914abbb93d0fa42ac227ee76db254")
	lifecycleDigestGolden = Digest("sha256:2e85b4edbd840e6989a43e2d7a8f344989258be15e4890e2a1c3c1626d3dd665")
	policyLowerDigest     = Digest("sha256:dcea4009bafddfcb08aae8d660338c222cf2fc78096c4fc4f60c8d032c3f3484")
	policyUpperDigest     = Digest("sha256:43e176e4f3b4bbb410d1a0c1f1393a6c327780859363f5ce05501e315682a0a8")
)

const activityOrdinaryGolden = `{"schema_version":1,"id":"99999999-9999-4999-8999-999999999991","class":"ordinary","actor":{"actor_kind":"agent","agent_id":"11111111-1111-4111-8111-111111111111","accountable_human_id":"22222222-2222-4222-8222-222222222222","session_id":"33333333-3333-4333-8333-333333333333","harness_name":"codex","harness_version":"1.0","model_name":"gpt","model_version":"5.6","assurance":"public-key-continuity","occurred_at":"2026-08-27T12:34:56.123456789Z"},"event":{"channel_id":"44444444-4444-4444-8444-444444444444","actor_id":"11111111-1111-4111-8111-111111111111","event_type":"task.status_changed","payload":{"from_status":"wip","task_id":"55555555-5555-4555-8555-555555555555","to_status":"done"},"note":"Task completed","created_at":"2026-08-27T12:34:56.123456789Z"},"created_at":"2026-08-27T12:34:56.123456789Z"}
`

const activityLifecycleGolden = `{"schema_version":1,"id":"99999999-9999-4999-8999-999999999991","class":"lifecycle","actor":{"actor_kind":"agent","agent_id":"11111111-1111-4111-8111-111111111111","accountable_human_id":"22222222-2222-4222-8222-222222222222","session_id":"33333333-3333-4333-8333-333333333333","harness_name":"codex","harness_version":"1.0","model_name":"gpt","model_version":"5.6","assurance":"public-key-continuity","occurred_at":"2026-08-27T12:34:56.123456789Z"},"lifecycle":{"kind":"delivery","reference_id":"99999999-9999-4999-8999-999999999992"},"created_at":"2026-08-27T12:34:56.123456789Z"}
`

const policyLowerGolden = `{"schema_version":1,"policy_version":1,"ordinary_max_age_seconds":2592000,"ordinary_max_rows":10000,"terminal_default_age_seconds":2592000,"terminal_maximum_age_seconds":31536000,"terminal_retention_seconds":2592000}
`

const policyUpperGolden = `{"schema_version":1,"policy_version":9007199254740991,"ordinary_max_age_seconds":2592000,"ordinary_max_rows":10000,"terminal_default_age_seconds":2592000,"terminal_maximum_age_seconds":31536000,"terminal_retention_seconds":31536000}
`

func activityTestTime() time.Time {
	return time.Date(2026, 8, 27, 12, 34, 56, 123456789, time.UTC)
}

func activityTestActor() types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind:          types.ActorAgent,
		AgentID:            activityAgentID,
		AccountableHumanID: activityHumanID,
		SessionID:          activitySessionID,
		HarnessName:        "codex",
		HarnessVersion:     "1.0",
		ModelName:          "gpt",
		ModelVersion:       "5.6",
		Assurance:          types.AssurancePublicKeyContinuity,
		OccurredAt:         activityTestTime(),
	}
}

func activityTestOrdinary() ActivityV1 {
	note := "Task completed"
	return ActivityV1{
		SchemaVersion: 1,
		ID:            activityTestID,
		Class:         ActivityOrdinaryV1,
		Actor:         activityTestActor(),
		Event: &ActivityEventProjectionV1{
			ChannelID: activityChannelID,
			ActorID:   activityAgentID,
			EventType: "task.status_changed",
			Payload:   json.RawMessage(`{"from_status":"wip","task_id":"55555555-5555-4555-8555-555555555555","to_status":"done"}`),
			Note:      &note,
			CreatedAt: activityTestTime(),
		},
		CreatedAt: activityTestTime(),
	}
}

func activityTestLifecycle() ActivityV1 {
	return ActivityV1{
		SchemaVersion: 1,
		ID:            activityTestID,
		Class:         ActivityLifecycleV1,
		Actor:         activityTestActor(),
		Lifecycle: &ActivityLifecycleProjectionV1{
			Kind:        ActivityLifecycleDeliveryV1,
			ReferenceID: activityReferenceID,
		},
		CreatedAt: activityTestTime(),
	}
}

func activityTestPolicy(retention, version int64) EffectiveActivityPolicyV1 {
	return EffectiveActivityPolicyV1{
		SchemaVersion:             1,
		PolicyVersion:             version,
		OrdinaryMaxAgeSeconds:     2592000,
		OrdinaryMaxRows:           10000,
		TerminalDefaultAgeSeconds: 2592000,
		TerminalMaximumAgeSeconds: 31536000,
		TerminalRetentionSeconds:  retention,
	}
}

func TestActivityV1CanonicalRoundTripAndDigest(t *testing.T) {
	for _, test := range []struct {
		name      string
		activity  ActivityV1
		canonical string
		digest    Digest
	}{
		{"ordinary", activityTestOrdinary(), activityOrdinaryGolden, activityDigestGolden},
		{"lifecycle", activityTestLifecycle(), activityLifecycleGolden, lifecycleDigestGolden},
	} {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := CanonicalActivity(test.activity)
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != test.canonical {
				t.Fatalf("CanonicalActivity differs\ngot  %q\nwant %q", canonical, test.canonical)
			}
			decoded, err := DecodeActivity(canonical)
			if err != nil || !reflect.DeepEqual(decoded, test.activity) {
				t.Fatalf("DecodeActivity = (%+v, %v), want original", decoded, err)
			}
			digest, err := DigestActivity(test.activity)
			if err != nil || digest != test.digest {
				t.Fatalf("DigestActivity = (%q, %v), want %q", digest, err, test.digest)
			}
		})
	}
}

func TestActivityV1RejectsUnknownNonCanonicalAndForgedAttribution(t *testing.T) {
	valid := activityTestOrdinary()
	canonical := []byte(activityOrdinaryGolden)
	reordered := bytes.Replace(canonical,
		[]byte(`{"schema_version":1,"id":"`+activityTestID+`"`),
		[]byte(`{"id":"`+activityTestID+`","schema_version":1`), 1)
	unknown := bytes.Replace(canonical, []byte(`,"created_at":`), []byte(`,"unexpected":true,"created_at":`), 1)

	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"unknown field", unknown},
		{"trailing JSON", append(bytes.Clone(canonical), []byte("{}\n")...)},
		{"leading whitespace", append([]byte(" "), canonical...)},
		{"member reordering", reordered},
		{"missing record", nil},
		{"missing fields", []byte("{}\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeActivity(test.raw); !errors.Is(err, ErrInvalidActivity) {
				t.Fatalf("DecodeActivity error = %v, want ErrInvalidActivity", err)
			}
		})
	}
	unknownVersion := bytes.Replace(canonical, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1)
	if _, err := DecodeActivity(unknownVersion); !errors.Is(err, ErrUnknownActivityVersion) {
		t.Fatalf("DecodeActivity unknown-version error = %v, want ErrUnknownActivityVersion", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*ActivityV1)
	}{
		{"noncanonical payload", func(activity *ActivityV1) {
			activity.Event.Payload = json.RawMessage(`{"task_id":"55555555-5555-4555-8555-555555555555","from_status":"wip","to_status":"done"}`)
		}},
		{"non-UTC time", func(activity *ActivityV1) {
			value := activity.CreatedAt.In(time.FixedZone("plus-one", 3600))
			activity.CreatedAt, activity.Actor.OccurredAt, activity.Event.CreatedAt = value, value, value
		}},
		{"unequal actor time", func(activity *ActivityV1) { activity.Actor.OccurredAt = activity.Actor.OccurredAt.Add(time.Second) }},
		{"unequal event time", func(activity *ActivityV1) { activity.Event.CreatedAt = activity.Event.CreatedAt.Add(time.Second) }},
		{"legacy assurance", func(activity *ActivityV1) { activity.Actor.Assurance = types.AssuranceLegacy }},
		{"unknown assurance", func(activity *ActivityV1) { activity.Actor.Assurance = types.AssuranceUnknown }},
		{"actor principal mismatch", func(activity *ActivityV1) { activity.Event.ActorID = activityHumanID }},
		{"invalid UTF-8 note", func(activity *ActivityV1) { note := string([]byte{0xff}); activity.Event.Note = &note }},
		{"NUL note", func(activity *ActivityV1) { note := "do\x00not"; activity.Event.Note = &note }},
		{"trimmed note", func(activity *ActivityV1) { note := " padded "; activity.Event.Note = &note }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := valid
			event := *valid.Event
			invalid.Event = &event
			test.mutate(&invalid)
			if _, err := CanonicalActivity(invalid); !errors.Is(err, ErrInvalidActivity) {
				t.Fatalf("CanonicalActivity error = %v, want ErrInvalidActivity", err)
			}
		})
	}

	leak := bytes.Replace(canonical, []byte(`"done"`), []byte(`"LEAK-ME"`), 1)
	if _, err := DecodeActivity(leak); err == nil || strings.Contains(err.Error(), "LEAK-ME") || strings.Contains(err.Error(), activityAgentID) {
		t.Fatalf("DecodeActivity returned unsafe error %q", err)
	}
}

func TestActivityV1RejectsInvalidClassLifecycleAndTypedPayloads(t *testing.T) {
	ordinary := activityTestOrdinary()
	presence := ordinary
	presence.Class, presence.Event = ActivityPresenceV1, nil
	if _, err := CanonicalActivity(presence); err != nil {
		t.Fatalf("CanonicalActivity(valid presence): %v", err)
	}

	validPayloads := []struct {
		eventType string
		payload   string
		note      *string
	}{
		{"task.status_changed", `{"from_status":"todo","task_id":"55555555-5555-4555-8555-555555555555","to_status":"wip"}`, stringPointer("transition")},
		{"review.requested", `{"author":"Harley","pr_url":"https://example.test/pull/1","repo":"wormhole"}`, nil},
		{"build.failed", `{"commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","error":"failed","repo":"wormhole"}`, nil},
		{"discovery.logged", `{"detail":"details","summary":"summary"}`, nil},
		{"message.posted", `{"text":"hello"}`, stringPointer("hello")},
	}
	for _, test := range validPayloads {
		t.Run("valid "+test.eventType, func(t *testing.T) {
			activity := ordinary
			event := *ordinary.Event
			activity.Event = &event
			activity.Event.EventType = test.eventType
			activity.Event.Payload = json.RawMessage(test.payload)
			activity.Event.Note = test.note
			if _, err := CanonicalActivity(activity); err != nil {
				t.Fatalf("CanonicalActivity error = %v", err)
			}
		})
	}

	for _, kind := range []ActivityLifecycleKindV1{
		ActivityLifecycleDeliveryV1,
		ActivityLifecycleConflictV1,
		ActivityLifecycleRecoveryV1,
		ActivityLifecycleReceiptV1,
	} {
		activity := activityTestLifecycle()
		activity.Lifecycle.Kind = kind
		if _, err := CanonicalActivity(activity); err != nil {
			t.Fatalf("CanonicalActivity(lifecycle %q): %v", kind, err)
		}
	}

	for _, test := range []struct {
		name   string
		mutate func(*ActivityV1)
	}{
		{"unknown class", func(activity *ActivityV1) { activity.Class = "future" }},
		{"presence event", func(activity *ActivityV1) { activity.Class = ActivityPresenceV1 }},
		{"ordinary without event", func(activity *ActivityV1) { activity.Event = nil }},
		{"ordinary with lifecycle", func(activity *ActivityV1) { activity.Lifecycle = activityTestLifecycle().Lifecycle }},
		{"lifecycle without lifecycle", func(activity *ActivityV1) { activity.Class = ActivityLifecycleV1; activity.Event = nil }},
		{"unknown lifecycle kind", func(activity *ActivityV1) { *activity = activityTestLifecycle(); activity.Lifecycle.Kind = "future" }},
		{"invalid lifecycle reference", func(activity *ActivityV1) {
			*activity = activityTestLifecycle()
			activity.Lifecycle.ReferenceID = "not-a-uuid"
		}},
		{"unknown event type", func(activity *ActivityV1) { activity.Event.EventType = "future.event" }},
		{"payload unknown field", func(activity *ActivityV1) {
			activity.Event.Payload = json.RawMessage(`{"extra":true,"from_status":"wip","task_id":"55555555-5555-4555-8555-555555555555","to_status":"done"}`)
		}},
		{"payload missing field", func(activity *ActivityV1) {
			activity.Event.Payload = json.RawMessage(`{"from_status":"wip","task_id":"55555555-5555-4555-8555-555555555555"}`)
		}},
		{"payload wrong type", func(activity *ActivityV1) {
			activity.Event.Payload = json.RawMessage(`{"from_status":"wip","task_id":5,"to_status":"done"}`)
		}},
		{"empty required field", func(activity *ActivityV1) {
			activity.Event.Payload = json.RawMessage(`{"from_status":"","task_id":"55555555-5555-4555-8555-555555555555","to_status":"done"}`)
		}},
		{"unknown task status", func(activity *ActivityV1) {
			activity.Event.Payload = json.RawMessage(`{"from_status":"queued","task_id":"55555555-5555-4555-8555-555555555555","to_status":"done"}`)
		}},
		{"equal task status", func(activity *ActivityV1) {
			activity.Event.Payload = json.RawMessage(`{"from_status":"wip","task_id":"55555555-5555-4555-8555-555555555555","to_status":"wip"}`)
		}},
		{"noncanonical task ID", func(activity *ActivityV1) {
			activity.Event.Payload = json.RawMessage(`{"from_status":"wip","task_id":"bad","to_status":"done"}`)
		}},
		{"message nil note", func(activity *ActivityV1) {
			activity.Event.EventType = "message.posted"
			activity.Event.Payload = json.RawMessage(`{"text":"hello"}`)
			activity.Event.Note = nil
		}},
		{"message empty note", func(activity *ActivityV1) {
			activity.Event.EventType = "message.posted"
			activity.Event.Payload = json.RawMessage(`{"text":"hello"}`)
			activity.Event.Note = stringPointer("")
		}},
		{"message trimmed note", func(activity *ActivityV1) {
			activity.Event.EventType = "message.posted"
			activity.Event.Payload = json.RawMessage(`{"text":"hello"}`)
			activity.Event.Note = stringPointer(" hello ")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			activity := ordinary
			event := *ordinary.Event
			activity.Event = &event
			test.mutate(&activity)
			if _, err := CanonicalActivity(activity); !errors.Is(err, ErrInvalidActivity) {
				t.Fatalf("CanonicalActivity error = %v, want ErrInvalidActivity", err)
			}
		})
	}

	invalidRequiredPayloads := []struct {
		eventType string
		payload   string
		note      *string
	}{
		{"review.requested", `{"author":"","pr_url":"https://example.test/pull/1","repo":"wormhole"}`, nil},
		{"build.failed", `{"commit_sha":"","error":"failed","repo":"wormhole"}`, nil},
		{"discovery.logged", `{"detail":"","summary":"summary"}`, nil},
		{"message.posted", `{"text":""}`, stringPointer("message")},
	}
	for _, test := range invalidRequiredPayloads {
		t.Run("invalid "+test.eventType, func(t *testing.T) {
			activity := ordinary
			event := *ordinary.Event
			activity.Event = &event
			activity.Event.EventType = test.eventType
			activity.Event.Payload = json.RawMessage(test.payload)
			activity.Event.Note = test.note
			if _, err := CanonicalActivity(activity); !errors.Is(err, ErrInvalidActivity) {
				t.Fatalf("CanonicalActivity error = %v, want ErrInvalidActivity", err)
			}
		})
	}
}

func TestEffectiveActivityPolicyCanonicalRoundTripAndDigest(t *testing.T) {
	for _, test := range []struct {
		name      string
		policy    EffectiveActivityPolicyV1
		canonical string
		digest    Digest
	}{
		{"lower bounds", activityTestPolicy(2592000, 1), policyLowerGolden, policyLowerDigest},
		{"upper bounds", activityTestPolicy(31536000, 9007199254740991), policyUpperGolden, policyUpperDigest},
	} {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := CanonicalActivityPolicy(test.policy)
			if err != nil || string(canonical) != test.canonical {
				t.Fatalf("CanonicalActivityPolicy = (%q, %v), want %q", canonical, err, test.canonical)
			}
			decoded, err := DecodeActivityPolicy(canonical)
			if err != nil || !reflect.DeepEqual(decoded, test.policy) {
				t.Fatalf("DecodeActivityPolicy = (%+v, %v), want original", decoded, err)
			}
			digest, err := DigestActivityPolicy(test.policy)
			if err != nil || digest != test.digest {
				t.Fatalf("DigestActivityPolicy = (%q, %v), want %q", digest, err, test.digest)
			}
		})
	}
}

func TestEffectiveActivityPolicyRejectsAbsentMalformedUnknownAndUnbounded(t *testing.T) {
	valid := activityTestPolicy(2592000, 1)
	for _, test := range []struct {
		name   string
		mutate func(*EffectiveActivityPolicyV1)
	}{
		{"zero schema", func(policy *EffectiveActivityPolicyV1) { policy.SchemaVersion = 0 }},
		{"unknown schema", func(policy *EffectiveActivityPolicyV1) { policy.SchemaVersion = 2 }},
		{"zero policy version", func(policy *EffectiveActivityPolicyV1) { policy.PolicyVersion = 0 }},
		{"negative policy version", func(policy *EffectiveActivityPolicyV1) { policy.PolicyVersion = -1 }},
		{"unsafe policy version", func(policy *EffectiveActivityPolicyV1) { policy.PolicyVersion = 9007199254740992 }},
		{"ordinary age absent", func(policy *EffectiveActivityPolicyV1) { policy.OrdinaryMaxAgeSeconds = 0 }},
		{"ordinary age variable", func(policy *EffectiveActivityPolicyV1) { policy.OrdinaryMaxAgeSeconds++ }},
		{"ordinary rows absent", func(policy *EffectiveActivityPolicyV1) { policy.OrdinaryMaxRows = 0 }},
		{"ordinary rows unbounded", func(policy *EffectiveActivityPolicyV1) { policy.OrdinaryMaxRows = -1 }},
		{"terminal default variable", func(policy *EffectiveActivityPolicyV1) { policy.TerminalDefaultAgeSeconds++ }},
		{"terminal maximum variable", func(policy *EffectiveActivityPolicyV1) { policy.TerminalMaximumAgeSeconds++ }},
		{"retention absent", func(policy *EffectiveActivityPolicyV1) { policy.TerminalRetentionSeconds = 0 }},
		{"retention below minimum", func(policy *EffectiveActivityPolicyV1) { policy.TerminalRetentionSeconds = 2591999 }},
		{"retention above maximum", func(policy *EffectiveActivityPolicyV1) { policy.TerminalRetentionSeconds = 31536001 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := valid
			test.mutate(&invalid)
			_, err := CanonicalActivityPolicy(invalid)
			if test.name == "unknown schema" {
				if !errors.Is(err, ErrUnknownActivityVersion) {
					t.Fatalf("error = %v, want ErrUnknownActivityVersion", err)
				}
			} else if !errors.Is(err, ErrInvalidActivityPolicy) {
				t.Fatalf("error = %v, want ErrInvalidActivityPolicy", err)
			}
		})
	}

	canonical := []byte(policyLowerGolden)
	unknown := bytes.Replace(canonical, []byte(`,"policy_version":`), []byte(`,"unknown":true,"policy_version":`), 1)
	reordered := bytes.Replace(canonical,
		[]byte(`{"schema_version":1,"policy_version":1`),
		[]byte(`{"policy_version":1,"schema_version":1`), 1)
	unsafe := bytes.Replace(canonical, []byte(`"policy_version":1`), []byte(`"policy_version":9007199254740992`), 1)
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"absent", nil},
		{"malformed", []byte(`{"schema_version":`)},
		{"unknown field", unknown},
		{"trailing", append(bytes.Clone(canonical), []byte("{}\n")...)},
		{"whitespace", append([]byte(" "), canonical...)},
		{"reordered", reordered},
		{"unsafe integer", unsafe},
		{"integer overflow", bytes.Replace(canonical, []byte(`"policy_version":1`), []byte(`"policy_version":9223372036854775808`), 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeActivityPolicy(test.raw); !errors.Is(err, ErrInvalidActivityPolicy) {
				t.Fatalf("DecodeActivityPolicy error = %v, want ErrInvalidActivityPolicy", err)
			}
		})
	}
}

func TestActivityReceiptV1CanonicalRoundTrip(t *testing.T) {
	receipt := ActivityReceiptV1{
		SchemaVersion:  1,
		ActivityID:     activityTestID,
		ActivityDigest: activityDigestGolden,
		Sequence:       42,
		PolicyVersion:  1,
		PolicyDigest:   policyLowerDigest,
		AcceptedAt:     activityTestTime(),
	}
	canonical, err := CanonicalActivityReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeActivityReceipt(canonical)
	if err != nil || !reflect.DeepEqual(decoded, receipt) {
		t.Fatalf("DecodeActivityReceipt = (%+v, %v), want original", decoded, err)
	}
}

func TestActivityReceiptV1RejectsUnsafeIntegersAndInvalidEvidence(t *testing.T) {
	valid := ActivityReceiptV1{
		SchemaVersion: 1, ActivityID: activityTestID, ActivityDigest: activityDigestGolden,
		Sequence: 42, PolicyVersion: 1, PolicyDigest: policyLowerDigest, AcceptedAt: activityTestTime(),
	}
	for _, test := range []struct {
		name   string
		mutate func(*ActivityReceiptV1)
	}{
		{"zero schema", func(receipt *ActivityReceiptV1) { receipt.SchemaVersion = 0 }},
		{"unknown schema", func(receipt *ActivityReceiptV1) { receipt.SchemaVersion = 2 }},
		{"invalid activity ID", func(receipt *ActivityReceiptV1) { receipt.ActivityID = "bad" }},
		{"invalid activity digest", func(receipt *ActivityReceiptV1) { receipt.ActivityDigest = "sha256:bad" }},
		{"zero sequence", func(receipt *ActivityReceiptV1) { receipt.Sequence = 0 }},
		{"unsafe sequence", func(receipt *ActivityReceiptV1) { receipt.Sequence = 9007199254740992 }},
		{"zero policy version", func(receipt *ActivityReceiptV1) { receipt.PolicyVersion = 0 }},
		{"unsafe policy version", func(receipt *ActivityReceiptV1) { receipt.PolicyVersion = 9007199254740992 }},
		{"invalid policy digest", func(receipt *ActivityReceiptV1) { receipt.PolicyDigest = "sha256:bad" }},
		{"zero accepted time", func(receipt *ActivityReceiptV1) { receipt.AcceptedAt = time.Time{} }},
		{"non-UTC accepted time", func(receipt *ActivityReceiptV1) {
			receipt.AcceptedAt = receipt.AcceptedAt.In(time.FixedZone("plus-one", 3600))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := valid
			test.mutate(&invalid)
			_, err := CanonicalActivityReceipt(invalid)
			if test.name == "unknown schema" {
				if !errors.Is(err, ErrUnknownActivityVersion) {
					t.Fatalf("error = %v, want ErrUnknownActivityVersion", err)
				}
			} else if !errors.Is(err, ErrInvalidActivity) {
				t.Fatalf("error = %v, want ErrInvalidActivity", err)
			}
		})
	}

	canonical, err := CanonicalActivityReceipt(valid)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(canonical, []byte(`,"activity_id":`), []byte(`,"unknown":true,"activity_id":`), 1)
	unsafe := bytes.Replace(canonical, []byte(`"sequence":42`), []byte(`"sequence":9007199254740992`), 1)
	for _, raw := range [][]byte{nil, []byte(`{"schema_version":`), unknown, append(bytes.Clone(canonical), []byte("{}\n")...), append([]byte(" "), canonical...), unsafe} {
		if _, err := DecodeActivityReceipt(raw); !errors.Is(err, ErrInvalidActivity) {
			t.Fatalf("DecodeActivityReceipt error = %v, want ErrInvalidActivity", err)
		}
	}
}

func TestActivityV1NeverEntersPortableStateOrReducer(t *testing.T) {
	for _, value := range []any{Snapshot{}, File{}, RecordValueV1{}, OperationV1{}} {
		typeOfValue := reflect.TypeOf(value)
		for index := 0; index < typeOfValue.NumField(); index++ {
			field := typeOfValue.Field(index)
			if strings.Contains(strings.ToLower(field.Name), "activity") || strings.Contains(strings.ToLower(field.Type.String()), "activity") {
				t.Fatalf("portable type %s contains Activity field %s %s", typeOfValue.Name(), field.Name, field.Type)
			}
		}
	}
	applyType := reflect.TypeOf(ApplyOperation)
	for index := 0; index < applyType.NumIn(); index++ {
		if strings.Contains(strings.ToLower(applyType.In(index).String()), "activity") {
			t.Fatalf("ApplyOperation input %d couples Activity into reducer", index)
		}
	}
	for index := 0; index < applyType.NumOut(); index++ {
		if strings.Contains(strings.ToLower(applyType.Out(index).String()), "activity") {
			t.Fatalf("ApplyOperation output %d couples Activity into reducer", index)
		}
	}
}
