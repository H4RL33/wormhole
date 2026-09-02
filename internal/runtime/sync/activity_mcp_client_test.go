package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type recordingActivityCaller struct {
	profile       types.FabricProfile
	credentialRef string
	tool          string
	arguments     json.RawMessage
	result        json.RawMessage
	err           error
	calls         int
}

func (r *recordingActivityCaller) CallActivity(_ context.Context, profile types.FabricProfile, credentialRef, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	r.calls++
	r.profile = profile
	r.credentialRef = credentialRef
	r.tool = tool
	r.arguments = append([]byte(nil), arguments...)
	if r.err != nil {
		return nil, r.err
	}
	return append([]byte(nil), r.result...), nil
}

func TestActivityPublicClientImplementsTransportInterface(t *testing.T) {
	var _ ActivityFabricClient = (*ActivityPublicClient)(nil)
	var _ ActivityClientFactory = (*ActivityPublicClientFactory)(nil)
	proofType := reflect.TypeOf(types.PublicRequestProof{})
	for _, method := range []any{
		(*ActivityPublicClient).Accept,
		(*ActivityPublicClient).Pull,
		(*ActivityPublicClient).SendPresence,
		(*ActivityPublicClient).Lifecycle,
		(*recordingActivityCaller).CallActivity,
	} {
		typeOf := reflect.TypeOf(method)
		for index := 0; index < typeOf.NumIn(); index++ {
			if typeOf.In(index) == proofType {
				t.Fatalf("method %v accepts unavailable proof", typeOf)
			}
		}
	}
	evidenceType := reflect.TypeOf(ActivityPullPolicyEvidence{})
	wantFields := []string{"PolicyJSON", "PolicyDigest"}
	if evidenceType.NumField() != len(wantFields) {
		t.Fatalf("ActivityPullPolicyEvidence fields=%d, want %d", evidenceType.NumField(), len(wantFields))
	}
	for index, want := range wantFields {
		if evidenceType.Field(index).Name != want {
			t.Fatalf("ActivityPullPolicyEvidence field[%d]=%s, want %s", index, evidenceType.Field(index).Name, want)
		}
	}
	clientType := reflect.TypeOf(ActivityPublicClient{})
	wantClientFields := []string{"caller", "profile", "credentialRef"}
	if clientType.NumField() != len(wantClientFields) {
		t.Fatalf("ActivityPublicClient fields=%d, want %d", clientType.NumField(), len(wantClientFields))
	}
	for index, want := range wantClientFields {
		if clientType.Field(index).Name != want {
			t.Fatalf("ActivityPublicClient field[%d]=%s, want %s", index, clientType.Field(index).Name, want)
		}
	}
}

func TestActivityPublicClientFactoryRejectsPrivateAndDoesNotRetainCredentialMaterial(t *testing.T) {
	caller := &recordingActivityCaller{result: json.RawMessage(`{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}]}`)}
	factory, err := NewActivityPublicClientFactory(caller)
	if err != nil {
		t.Fatal(err)
	}
	profile := types.FabricProfile{
		ProfileID:        "10000000-0000-4000-8000-000000000001",
		Alias:            "public",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001",
		BaseURL:          "https://fabric.example.test",
		Mode:             types.FabricModePublic,
		CredentialRef:    "keyring:public",
	}
	client, err := factory.Client(context.Background(), profile, "SECRET-MATERIAL")
	if err != nil {
		t.Fatal(err)
	}
	concrete := client.(*ActivityPublicClient)
	_, err = concrete.Lifecycle(context.Background(), ActivityLifecycleRequest{
		AttachmentRef: "50000000-0000-4000-8000-000000000001",
		ActivityID:    "a0000000-0000-4000-8000-000000000001",
		Change: localstore.ActivityLifecycleChange{
			Kind:          "delivery",
			ReferenceID:   "a0000000-0000-4000-8000-000000000001",
			ExpectedState: "pending",
			NextState:     "delivered",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	combined := fmt.Sprintf("%#v %#v %s", concrete, caller, string(caller.arguments))
	if strings.Contains(combined, "SECRET-MATERIAL") || caller.credentialRef != profile.CredentialRef {
		t.Fatalf("credential boundary leaked: %s", combined)
	}
	profile.Mode = types.FabricModePrivate
	before := caller.calls
	got, err := factory.Client(context.Background(), profile, "SECRET-MATERIAL")
	if got != nil || !errors.Is(err, ErrFabricUnavailable) || caller.calls != before {
		t.Fatalf("private client=(%#v,%v) calls=%d/%d", got, err, caller.calls, before)
	}
}

func TestActivityMCPClientStrictToolResult(t *testing.T) {
	op := "wormhole.activity.lifecycle"
	tests := []struct {
		name     string
		raw      string
		wantBody string
		want     error
	}{
		{"success absent flag", `{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}]}`, `{"version":1,"state":"delivered"}`, nil},
		{"success false flag", `{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}],"isError":false}`, `{"version":1,"state":"delivered"}`, nil},
		{"null error flag", `{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}],"isError":null}`, ``, ErrFabricUnavailable},
		{"closed failure", `{"content":[{"type":"text","text":"{\"code\":\"activity_lifecycle_conflict\",\"operation\":\"wormhole.activity.lifecycle\"}"}],"isError":true}`, ``, localstore.ErrActivityLifecycleConflict},
		{"wrong failure operation", `{"content":[{"type":"text","text":"{\"code\":\"activity_lifecycle_conflict\",\"operation\":\"wormhole.activity.pull\"}"}],"isError":true}`, ``, ErrFabricUnavailable},
		{"duplicate wrapper", `{"content":[],"content":[]}`, ``, ErrFabricUnavailable},
		{"duplicate inner", `{"content":[{"type":"text","text":"{\"code\":\"activity_not_found\",\"code\":\"internal_error\",\"operation\":\"wormhole.activity.lifecycle\"}"}],"isError":true}`, ``, ErrFabricUnavailable},
		{"unknown wrapper", `{"content":[],"extra":true}`, ``, ErrFabricUnavailable},
		{"outer JSON-RPC envelope", `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`, ``, ErrFabricUnavailable},
		{"unknown content member", `{"content":[{"type":"text","text":"{}","extra":true}]}`, ``, ErrFabricUnavailable},
		{"two items", `{"content":[{"type":"text","text":"{}"},{"type":"text","text":"{}"}]}`, ``, ErrFabricUnavailable},
		{"non text", `{"content":[{"type":"image","text":"{}"}]}`, ``, ErrFabricUnavailable},
		{"trailing", `{"content":[]} {}`, ``, ErrFabricUnavailable},
		{"error with success", `{"content":[{"type":"text","text":"{\"version\":1,\"state\":\"delivered\"}"}],"isError":true}`, ``, ErrFabricUnavailable},
		{"success with failure", `{"content":[{"type":"text","text":"{\"code\":\"activity_not_found\",\"operation\":\"wormhole.activity.lifecycle\"}"}]}`, ``, ErrFabricUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := unwrapActivityToolResult(json.RawMessage(test.raw), op)
			if !errors.Is(err, test.want) || string(got) != test.wantBody {
				t.Fatalf("unwrap=(%s,%v), want (%s,%v)", got, err, test.wantBody, test.want)
			}
		})
	}
}

func TestActivityPublicClientConvertsAcceptResultsAndCanonicalArguments(t *testing.T) {
	activity, activityJSON, activityDigest := activityMCPTestActivity(t, activityTestIDOne)
	policy := activityTestPolicy(1)
	policyJSON, policyDigest := activityTestPolicyEvidence(t, policy)
	receipt := projectstate.ActivityReceiptV1{
		SchemaVersion:  1,
		ActivityID:     activity.ID,
		ActivityDigest: activityDigest,
		Sequence:       7,
		PolicyVersion:  policy.PolicyVersion,
		PolicyDigest:   policyDigest,
		AcceptedAt:     time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC),
	}
	caller := &recordingActivityCaller{result: activityMCPResult(t, ActivityAcceptedV1Result{
		Version:                 1,
		Status:                  "accepted",
		Receipt:                 receipt,
		EffectiveActivityPolicy: policy,
		PolicyDigest:            policyDigest,
	}, false)}
	client := activityMCPTestClient(t, caller)
	request := ActivityAcceptRequest{
		AttachmentRef:  "50000000-0000-4000-8000-000000000001",
		PolicyVersion:  policy.PolicyVersion,
		PolicyDigest:   policyDigest,
		ActivityJSON:   activityJSON,
		ActivityDigest: activityDigest,
	}
	got, err := client.Accept(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got.Receipt != receipt || !bytes.Equal(got.PolicyJSON, policyJSON) || got.PolicyDigest != policyDigest || got.PolicyChanged {
		t.Fatalf("Accept=%+v", got)
	}
	assertActivityMCPCall(t, caller, "wormhole.activity.accept", ActivityAcceptV1Args{
		Version:        1,
		AttachmentRef:  request.AttachmentRef,
		PolicyVersion:  request.PolicyVersion,
		PolicyDigest:   request.PolicyDigest,
		Activity:       activity,
		ActivityDigest: activityDigest,
	})

	caller.result = activityMCPResult(t, ActivityPolicyChangedV1Result{
		Version:                 1,
		Status:                  "policy_changed",
		EffectiveActivityPolicy: activityTestPolicy(2),
		PolicyDigest:            activityMCPPolicyDigest(t, activityTestPolicy(2)),
	}, false)
	got, err = client.Accept(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantChangedJSON, wantChangedDigest := activityTestPolicyEvidence(t, activityTestPolicy(2))
	if got.Receipt != (projectstate.ActivityReceiptV1{}) || !got.PolicyChanged || !bytes.Equal(got.PolicyJSON, wantChangedJSON) || got.PolicyDigest != wantChangedDigest {
		t.Fatalf("policy-changed Accept=%+v", got)
	}
}

func TestActivityPublicClientConvertsPresenceResults(t *testing.T) {
	activity := activityTestPresence(activityTestIDOne, time.Date(2026, 8, 30, 13, 1, 0, 0, time.UTC))
	activity.Actor.Assurance = types.AssurancePublicKeyContinuity
	activityJSON, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	activityDigest, err := projectstate.DigestActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	policy := activityTestPolicy(1)
	_, policyDigest := activityTestPolicyEvidence(t, policy)
	caller := &recordingActivityCaller{result: activityMCPResult(t, ActivityPresenceAcceptedV1Result{Version: 1, Status: "accepted"}, false)}
	client := activityMCPTestClient(t, caller)
	request := ActivityPresenceRequest{
		AttachmentRef:  "50000000-0000-4000-8000-000000000001",
		PolicyVersion:  policy.PolicyVersion,
		PolicyDigest:   policyDigest,
		ActivityJSON:   activityJSON,
		ActivityDigest: activityDigest,
	}
	got, err := client.SendPresence(context.Background(), request)
	if err != nil || len(got.PolicyJSON) != 0 || got.PolicyDigest != "" || got.PolicyChanged {
		t.Fatalf("accepted presence=(%+v,%v)", got, err)
	}
	assertActivityMCPCall(t, caller, "wormhole.activity.presence", ActivityPresenceV1Args{
		Version:        1,
		AttachmentRef:  request.AttachmentRef,
		PolicyVersion:  request.PolicyVersion,
		PolicyDigest:   request.PolicyDigest,
		Activity:       activity,
		ActivityDigest: activityDigest,
	})

	changed := activityTestPolicy(2)
	changedJSON, changedDigest := activityTestPolicyEvidence(t, changed)
	caller.result = activityMCPResult(t, ActivityPolicyChangedV1Result{
		Version:                 1,
		Status:                  "policy_changed",
		EffectiveActivityPolicy: changed,
		PolicyDigest:            changedDigest,
	}, false)
	got, err = client.SendPresence(context.Background(), request)
	if err != nil || !got.PolicyChanged || !bytes.Equal(got.PolicyJSON, changedJSON) || got.PolicyDigest != changedDigest {
		t.Fatalf("changed presence=(%+v,%v)", got, err)
	}
}

func TestActivityPublicClientPresenceAcceptedVariantRequiresExactStatus(t *testing.T) {
	activity := activityTestPresence(activityTestIDOne, time.Date(2026, 8, 30, 13, 1, 30, 0, time.UTC))
	activity.Actor.Assurance = types.AssurancePublicKeyContinuity
	activityJSON, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	activityDigest, err := projectstate.DigestActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	_, policyDigest := activityTestPolicyEvidence(t, activityTestPolicy(1))
	request := ActivityPresenceRequest{
		AttachmentRef:  "50000000-0000-4000-8000-000000000001",
		PolicyVersion:  1,
		PolicyDigest:   policyDigest,
		ActivityJSON:   activityJSON,
		ActivityDigest: activityDigest,
	}
	const secret = "SECRET-RAW-PRESENCE-RESULT"
	for _, test := range []struct {
		name string
		text string
		want error
	}{
		{"accepted", `{"version":1,"status":"accepted"}`, nil},
		{"wrong", `{"version":1,"status":"unexpected SECRET-RAW-PRESENCE-RESULT"}`, ErrFabricUnavailable},
		{"empty", `{"version":1,"status":""}`, ErrFabricUnavailable},
		{"null", `{"version":1,"status":null}`, ErrFabricUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			caller := &recordingActivityCaller{result: activityMCPTextResult(t, test.text, false)}
			client := activityMCPTestClient(t, caller)
			got, err := client.SendPresence(context.Background(), request)
			if !errors.Is(err, test.want) || len(got.PolicyJSON) != 0 || got.PolicyDigest != "" || got.PolicyChanged || caller.calls != 1 {
				t.Fatalf("SendPresence=(%+v,%v), calls=%d, want zero/%v/1", got, err, caller.calls, test.want)
			}
			if strings.Contains(fmt.Sprint(err), secret) || strings.Contains(fmt.Sprint(err), test.text) {
				t.Fatalf("raw result leaked: %v", err)
			}
		})
	}
}

func TestActivityPublicClientConvertsPullEvidenceAndOpaqueSource(t *testing.T) {
	current := activityTestPolicy(3)
	currentJSON, currentDigest := activityTestPolicyEvidence(t, current)
	historical := activityTestPolicy(2)
	_, historicalDigest := activityTestPolicyEvidence(t, historical)
	activity, activityJSON, activityDigest := activityMCPTestActivity(t, activityTestIDOne)
	receipt := projectstate.ActivityReceiptV1{
		SchemaVersion:  1,
		ActivityID:     activity.ID,
		ActivityDigest: activityDigest,
		Sequence:       5,
		PolicyVersion:  historical.PolicyVersion,
		PolicyDigest:   historicalDigest,
		AcceptedAt:     time.Date(2026, 8, 30, 13, 2, 0, 0, time.UTC),
	}
	sourceRef := "b0000000-0000-4000-8000-000000000001"
	value := ActivityPullV1Result{
		Version:         1,
		EffectivePolicy: current,
		PolicyDigest:    currentDigest,
		HistoricalPolicies: []ActivityPolicyEvidenceV1{{
			Policy: historical, PolicyDigest: historicalDigest,
		}},
		Deliveries: []ActivityDeliveryV1{{
			SourceRef: sourceRef, Activity: activity, ActivityDigest: activityDigest, Receipt: receipt,
		}},
		NextSequence: 5,
		HasMore:      true,
	}
	caller := &recordingActivityCaller{result: activityMCPResult(t, value, false)}
	client := activityMCPTestClient(t, caller)
	request := ActivityPullRequest{
		AttachmentRef: "50000000-0000-4000-8000-000000000001",
		AfterSequence: 3,
		Limit:         2,
	}
	got, err := client.Pull(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.PolicyJSON, currentJSON) || got.PolicyDigest != currentDigest || got.NextSequence != 5 || !got.HasMore || len(got.HistoricalPolicies) != 1 || len(got.Deliveries) != 1 {
		t.Fatalf("Pull=%+v", got)
	}
	historicalJSON, _ := activityTestPolicyEvidence(t, historical)
	if !bytes.Equal(got.HistoricalPolicies[0].PolicyJSON, historicalJSON) || got.HistoricalPolicies[0].PolicyDigest != historicalDigest {
		t.Fatalf("historical policy=%+v", got.HistoricalPolicies[0])
	}
	receiptJSON, err := projectstate.CanonicalActivityReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Deliveries[0].SourceWorkspaceID != types.WorkspaceID(sourceRef) || !bytes.Equal(got.Deliveries[0].ActivityJSON, activityJSON) || got.Deliveries[0].ActivityDigest != activityDigest || !bytes.Equal(got.Deliveries[0].ReceiptJSON, receiptJSON) {
		t.Fatalf("delivery=%+v", got.Deliveries[0])
	}
	assertActivityMCPCall(t, caller, "wormhole.activity.pull", ActivityPullV1Args{
		Version: 1, AttachmentRef: request.AttachmentRef, AfterSequence: request.AfterSequence, Limit: request.Limit,
	})
}

func TestActivityMCPClientSafeFailureMappingAndRedaction(t *testing.T) {
	op := "wormhole.activity.pull"
	tests := []struct {
		name string
		code string
		want error
	}{
		{"invalid", "invalid_activity", projectstate.ErrInvalidActivity},
		{"unknown version", "unknown_activity_version", projectstate.ErrUnknownActivityVersion},
		{"policy required", "activity_policy_required", localstore.ErrActivityPolicyUnavailable},
		{"policy changed", "activity_policy_changed", localstore.ErrActivityPolicyChanged},
		{"not found", "activity_not_found", localstore.ErrActivityNotFound},
		{"replay", "activity_replay_conflict", localstore.ErrActivityReplayConflict},
		{"cursor", "activity_cursor_invalid", localstore.ErrActivityCursorConflict},
		{"lifecycle", "activity_lifecycle_conflict", localstore.ErrActivityLifecycleConflict},
		{"authentication", "authentication_failed", ErrAttentionRequired},
		{"attachment", "attachment_not_found", ErrAttentionRequired},
		{"internal", "internal_error", ErrFabricUnavailable},
		{"unknown", "future_code", ErrFabricUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			secret := "SECRET credential=keyring:x proof=abc attachment=50000000-0000-4000-8000-000000000001"
			inner := fmt.Sprintf(`{"code":%q,"operation":%q}`, test.code, op)
			raw, err := json.Marshal(activityToolResult{Content: []activityToolContent{{Type: "text", Text: inner}}, IsError: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := unwrapActivityToolResult(append(raw, []byte(" "+secret)...), op)
			if got != nil || !errors.Is(err, ErrFabricUnavailable) {
				t.Fatalf("trailing secret result=(%q,%v)", got, err)
			}
			got, err = unwrapActivityToolResult(raw, op)
			if got != nil || !errors.Is(err, test.want) {
				t.Fatalf("mapped result=(%q,%v), want %v", got, err, test.want)
			}
			if strings.Contains(fmt.Sprint(err), secret) {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
}

func TestActivityPublicClientRejectsInvalidInputsBeforeCaller(t *testing.T) {
	activity, activityJSON, activityDigest := activityMCPTestActivity(t, activityTestIDOne)
	policy := activityTestPolicy(1)
	_, policyDigest := activityTestPolicyEvidence(t, policy)
	tests := []struct {
		name string
		call func(*ActivityPublicClient) error
		want error
	}{
		{
			name: "noncanonical accept activity",
			call: func(client *ActivityPublicClient) error {
				_, err := client.Accept(context.Background(), ActivityAcceptRequest{
					AttachmentRef: "50000000-0000-4000-8000-000000000001", PolicyVersion: 1, PolicyDigest: policyDigest,
					ActivityJSON: bytes.TrimSuffix(activityJSON, []byte{'\n'}), ActivityDigest: activityDigest,
				})
				return err
			},
			want: projectstate.ErrInvalidActivity,
		},
		{
			name: "accept digest mismatch",
			call: func(client *ActivityPublicClient) error {
				_, err := client.Accept(context.Background(), ActivityAcceptRequest{
					AttachmentRef: "50000000-0000-4000-8000-000000000001", PolicyVersion: 1, PolicyDigest: policyDigest,
					ActivityJSON: activityJSON, ActivityDigest: projectstate.Digest("sha256:" + strings.Repeat("f", 64)),
				})
				return err
			},
			want: projectstate.ErrInvalidActivity,
		},
		{
			name: "presence requires presence class",
			call: func(client *ActivityPublicClient) error {
				_, err := client.SendPresence(context.Background(), ActivityPresenceRequest{
					AttachmentRef: "50000000-0000-4000-8000-000000000001", PolicyVersion: 1, PolicyDigest: policyDigest,
					ActivityJSON: activityJSON, ActivityDigest: activityDigest,
				})
				return err
			},
			want: projectstate.ErrInvalidActivity,
		},
		{
			name: "pull cursor below zero",
			call: func(client *ActivityPublicClient) error {
				_, err := client.Pull(context.Background(), ActivityPullRequest{AttachmentRef: "50000000-0000-4000-8000-000000000001", AfterSequence: -1, Limit: 1})
				return err
			},
			want: localstore.ErrActivityCursorConflict,
		},
		{
			name: "pull cursor above safe integer",
			call: func(client *ActivityPublicClient) error {
				_, err := client.Pull(context.Background(), ActivityPullRequest{AttachmentRef: "50000000-0000-4000-8000-000000000001", AfterSequence: 9_007_199_254_740_992, Limit: 1})
				return err
			},
			want: localstore.ErrActivityCursorConflict,
		},
		{
			name: "pull limit above bound",
			call: func(client *ActivityPublicClient) error {
				_, err := client.Pull(context.Background(), ActivityPullRequest{AttachmentRef: "50000000-0000-4000-8000-000000000001", Limit: 501})
				return err
			},
			want: localstore.ErrActivityCursorConflict,
		},
		{
			name: "lifecycle invalid reference",
			call: func(client *ActivityPublicClient) error {
				_, err := client.Lifecycle(context.Background(), ActivityLifecycleRequest{
					AttachmentRef: "50000000-0000-4000-8000-000000000001", ActivityID: activity.ID,
					Change: localstore.ActivityLifecycleChange{Kind: "delivery", ReferenceID: "bad", ExpectedState: "pending", NextState: "delivered"},
				})
				return err
			},
			want: localstore.ErrActivityLifecycleConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caller := &recordingActivityCaller{}
			client := activityMCPTestClient(t, caller)
			err := test.call(client)
			if !errors.Is(err, test.want) || caller.calls != 0 {
				t.Fatalf("error/calls=(%v,%d), want (%v,0)", err, caller.calls, test.want)
			}
		})
	}
}

func TestActivityPublicClientRejectsMalformedResultUnions(t *testing.T) {
	_, activityJSON, activityDigest := activityMCPTestActivity(t, activityTestIDOne)
	policy := activityTestPolicy(1)
	_, policyDigest := activityTestPolicyEvidence(t, policy)
	request := ActivityAcceptRequest{
		AttachmentRef: "50000000-0000-4000-8000-000000000001", PolicyVersion: 1, PolicyDigest: policyDigest,
		ActivityJSON: activityJSON, ActivityDigest: activityDigest,
	}
	tests := []struct {
		name string
		text string
		want error
	}{
		{"unknown status", `{"version":1,"status":"future"}`, ErrFabricUnavailable},
		{"unknown accepted member", `{"version":1,"status":"accepted","receipt":{},"effective_activity_policy":{},"policy_digest":"","extra":true}`, ErrFabricUnavailable},
		{"duplicate nested policy member", `{"version":1,"status":"policy_changed","effective_activity_policy":{"schema_version":1,"policy_version":1,"policy_version":1,"ordinary_max_age_seconds":2592000,"ordinary_max_rows":10000,"terminal_default_age_seconds":2592000,"terminal_maximum_age_seconds":31536000,"terminal_retention_seconds":2592000},"policy_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, ErrFabricUnavailable},
		{"trailing inner JSON", `{"version":1,"status":"policy_changed"} {}`, ErrFabricUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caller := &recordingActivityCaller{result: activityMCPTextResult(t, test.text, false)}
			client := activityMCPTestClient(t, caller)
			got, err := client.Accept(context.Background(), request)
			if !reflect.DeepEqual(got, ActivityAcceptResponse{}) || !errors.Is(err, test.want) {
				t.Fatalf("Accept=(%+v,%v), want zero/%v", got, err, test.want)
			}
		})
	}
}

func TestActivityPublicClientRejectsInconsistentAcceptEvidence(t *testing.T) {
	activity, activityJSON, activityDigest := activityMCPTestActivity(t, activityTestIDOne)
	policy := activityTestPolicy(1)
	_, policyDigest := activityTestPolicyEvidence(t, policy)
	receipt := projectstate.ActivityReceiptV1{
		SchemaVersion: 1, ActivityID: activity.ID, ActivityDigest: activityDigest, Sequence: 1,
		PolicyVersion: 1, PolicyDigest: policyDigest, AcceptedAt: time.Date(2026, 8, 30, 13, 3, 0, 0, time.UTC),
	}
	request := ActivityAcceptRequest{
		AttachmentRef: "50000000-0000-4000-8000-000000000001", PolicyVersion: 1, PolicyDigest: policyDigest,
		ActivityJSON: activityJSON, ActivityDigest: activityDigest,
	}
	tests := []struct {
		name   string
		mutate func(*ActivityAcceptedV1Result)
		want   error
	}{
		{"receipt activity mismatch", func(value *ActivityAcceptedV1Result) { value.Receipt.ActivityID = activityTestIDTwo }, localstore.ErrActivityReplayConflict},
		{"receipt policy version mismatch", func(value *ActivityAcceptedV1Result) { value.Receipt.PolicyVersion = 2 }, localstore.ErrActivityReplayConflict},
		{"receipt policy digest mismatch", func(value *ActivityAcceptedV1Result) {
			value.Receipt.PolicyDigest = projectstate.Digest("sha256:" + strings.Repeat("f", 64))
		}, localstore.ErrActivityReplayConflict},
		{"effective policy digest mismatch", func(value *ActivityAcceptedV1Result) {
			value.PolicyDigest = projectstate.Digest("sha256:" + strings.Repeat("f", 64))
		}, localstore.ErrActivityPolicyUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := ActivityAcceptedV1Result{Version: 1, Status: "accepted", Receipt: receipt, EffectiveActivityPolicy: policy, PolicyDigest: policyDigest}
			test.mutate(&value)
			caller := &recordingActivityCaller{result: activityMCPResult(t, value, false)}
			client := activityMCPTestClient(t, caller)
			_, err := client.Accept(context.Background(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Accept error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestActivityPublicClientRejectsInvalidPullEvidence(t *testing.T) {
	base, request := activityMCPPullFixture(t)
	tests := []struct {
		name   string
		mutate func(*ActivityPullV1Result, *ActivityPullRequest)
		want   error
	}{
		{"historical policies null", func(value *ActivityPullV1Result, _ *ActivityPullRequest) { value.HistoricalPolicies = nil }, ErrFabricUnavailable},
		{"deliveries null", func(value *ActivityPullV1Result, _ *ActivityPullRequest) { value.Deliveries = nil }, ErrFabricUnavailable},
		{"out of order historical policy", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			first := value.HistoricalPolicies[0]
			older := activityTestPolicy(1)
			value.HistoricalPolicies = []ActivityPolicyEvidenceV1{first, {Policy: older, PolicyDigest: activityMCPPolicyDigest(t, older)}}
		}, localstore.ErrActivityReplayConflict},
		{"duplicate historical policy", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.HistoricalPolicies = append(value.HistoricalPolicies, value.HistoricalPolicies[0])
		}, localstore.ErrActivityReplayConflict},
		{"future historical policy", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			future := activityTestPolicy(4)
			value.HistoricalPolicies[0] = ActivityPolicyEvidenceV1{Policy: future, PolicyDigest: activityMCPPolicyDigest(t, future)}
		}, localstore.ErrActivityReplayConflict},
		{"too many deliveries", func(value *ActivityPullV1Result, request *ActivityPullRequest) {
			request.Limit = 1
			value.Deliveries = append(value.Deliveries, value.Deliveries[0])
		}, localstore.ErrActivityCursorConflict},
		{"invalid opaque source", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.Deliveries[0].SourceRef = "not-a-uuid"
		}, localstore.ErrActivityReplayConflict},
		{"activity digest mismatch", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.Deliveries[0].ActivityDigest = projectstate.Digest("sha256:" + strings.Repeat("f", 64))
		}, localstore.ErrActivityReplayConflict},
		{"receipt activity mismatch", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.Deliveries[0].Receipt.ActivityID = activityTestIDTwo
		}, localstore.ErrActivityReplayConflict},
		{"receipt sequence not increasing", func(value *ActivityPullV1Result, request *ActivityPullRequest) {
			value.Deliveries[0].Receipt.Sequence = request.AfterSequence
		}, localstore.ErrActivityReplayConflict},
		{"receipt policy above current", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.Deliveries[0].Receipt.PolicyVersion = 4
		}, localstore.ErrActivityReplayConflict},
		{"missing historical policy", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.HistoricalPolicies = []ActivityPolicyEvidenceV1{}
		}, localstore.ErrActivityPolicyUnavailable},
		{"historical digest mismatch", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.HistoricalPolicies[0].PolicyDigest = projectstate.Digest("sha256:" + strings.Repeat("f", 64))
		}, localstore.ErrActivityReplayConflict},
		{"receipt policy digest mismatch", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.Deliveries[0].Receipt.PolicyDigest = projectstate.Digest("sha256:" + strings.Repeat("f", 64))
		}, localstore.ErrActivityPolicyUnavailable},
		{"next below delivery", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.NextSequence = value.Deliveries[0].Receipt.Sequence - 1
		}, localstore.ErrActivityCursorConflict},
		{"has more empty", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.Deliveries = []ActivityDeliveryV1{}
			value.HistoricalPolicies = []ActivityPolicyEvidenceV1{}
			value.HasMore = true
		}, localstore.ErrActivityCursorConflict},
		{"has more next mismatch", func(value *ActivityPullV1Result, _ *ActivityPullRequest) { value.HasMore = true; value.NextSequence++ }, localstore.ErrActivityCursorConflict},
		{"next above safe integer", func(value *ActivityPullV1Result, _ *ActivityPullRequest) {
			value.HasMore = false
			value.NextSequence = 9_007_199_254_740_992
		}, localstore.ErrActivityCursorConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			value.HistoricalPolicies = append([]ActivityPolicyEvidenceV1(nil), base.HistoricalPolicies...)
			value.Deliveries = append([]ActivityDeliveryV1(nil), base.Deliveries...)
			requestCopy := request
			test.mutate(&value, &requestCopy)
			caller := &recordingActivityCaller{result: activityMCPResult(t, value, false)}
			client := activityMCPTestClient(t, caller)
			_, err := client.Pull(context.Background(), requestCopy)
			if !errors.Is(err, test.want) {
				t.Fatalf("Pull error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestActivityPublicClientCollapsesCallerErrorsAndLifecycleMismatch(t *testing.T) {
	secret := "SECRET proof=abc credential=xyz"
	caller := &recordingActivityCaller{err: errors.New(secret)}
	client := activityMCPTestClient(t, caller)
	request := ActivityLifecycleRequest{
		AttachmentRef: "50000000-0000-4000-8000-000000000001", ActivityID: activityTestIDOne,
		Change: localstore.ActivityLifecycleChange{Kind: "delivery", ReferenceID: activityTestIDOne, ExpectedState: "pending", NextState: "delivered"},
	}
	_, err := client.Lifecycle(context.Background(), request)
	if !errors.Is(err, ErrFabricUnavailable) || strings.Contains(fmt.Sprint(err), secret) {
		t.Fatalf("caller error=%v", err)
	}
	caller.err = nil
	caller.result = activityMCPResult(t, ActivityLifecycleV1Result{Version: 1, State: "cancelled"}, false)
	_, err = client.Lifecycle(context.Background(), request)
	if !errors.Is(err, localstore.ErrActivityLifecycleConflict) {
		t.Fatalf("lifecycle mismatch=%v", err)
	}
}

func TestActivityPublicClientFactoryRejectsInvalidConstructionWithoutCaller(t *testing.T) {
	if got, err := NewActivityPublicClientFactory(nil); got != nil || !errors.Is(err, ErrFabricUnavailable) {
		t.Fatalf("nil factory=(%#v,%v)", got, err)
	}
	caller := &recordingActivityCaller{}
	factory, err := NewActivityPublicClientFactory(caller)
	if err != nil {
		t.Fatal(err)
	}
	valid := types.FabricProfile{
		ProfileID: "10000000-0000-4000-8000-000000000001", Alias: "public", FabricInstanceID: "20000000-0000-4000-8000-000000000001",
		BaseURL: "https://fabric.example.test", Mode: types.FabricModePublic, CredentialRef: "keyring:public",
	}
	for _, test := range []struct {
		name     string
		profile  types.FabricProfile
		material string
	}{
		{"empty material", valid, ""},
		{"empty credential reference", func() types.FabricProfile { value := valid; value.CredentialRef = ""; return value }(), "secret"},
		{"invalid profile", func() types.FabricProfile { value := valid; value.ProfileID = "bad"; return value }(), "secret"},
		{"private profile", func() types.FabricProfile { value := valid; value.Mode = types.FabricModePrivate; return value }(), "secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := factory.Client(context.Background(), test.profile, test.material)
			if got != nil || !errors.Is(err, ErrFabricUnavailable) || caller.calls != 0 {
				t.Fatalf("Client=(%#v,%v), calls=%d", got, err, caller.calls)
			}
		})
	}
}

func TestActivityTransportInjectsFreshRouteIntoValidatedPullPolicyEvidence(t *testing.T) {
	value, request := activityMCPPullFixture(t)
	wire, err := runtimePullResponse(value, request)
	if err != nil {
		t.Fatal(err)
	}
	route := types.ActivityRouteKey{
		ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "00000000-0000-4000-8000-000000000011",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001", RemoteProjectID: "30000000-0000-4000-8000-000000000001",
		StreamID: "40000000-0000-4000-8000-000000000001", CanonicalRef: "refs/heads/main",
	}
	deliveries, policies, err := validatePulledBatchForProfile(wire.Deliveries, wire.HistoricalPolicies, route, types.FabricModePublic, value.EffectivePolicy.PolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || len(policies) != 1 || policies[0].Route != route {
		t.Fatalf("validated pull=(%+v,%+v)", deliveries, policies)
	}
	corrupt := append([]ActivityPullPolicyEvidence(nil), wire.HistoricalPolicies...)
	corrupt[0].PolicyDigest = projectstate.Digest("sha256:" + strings.Repeat("f", 64))
	deliveries, policies, err = validatePulledBatchForProfile(wire.Deliveries, corrupt, route, types.FabricModePublic, value.EffectivePolicy.PolicyVersion)
	if !errors.Is(err, localstore.ErrActivityPolicyUnavailable) || deliveries != nil || policies != nil {
		t.Fatalf("corrupt evidence=(%+v,%+v,%v)", deliveries, policies, err)
	}
}

func activityMCPTestClient(t *testing.T, caller *recordingActivityCaller) *ActivityPublicClient {
	t.Helper()
	factory, err := NewActivityPublicClientFactory(caller)
	if err != nil {
		t.Fatal(err)
	}
	client, err := factory.Client(context.Background(), types.FabricProfile{
		ProfileID:        "10000000-0000-4000-8000-000000000001",
		Alias:            "public",
		FabricInstanceID: "20000000-0000-4000-8000-000000000001",
		BaseURL:          "https://fabric.example.test",
		Mode:             types.FabricModePublic,
		CredentialRef:    "keyring:public",
	}, "resolved material")
	if err != nil {
		t.Fatal(err)
	}
	return client.(*ActivityPublicClient)
}

func activityMCPTestActivity(t *testing.T, id string) (projectstate.ActivityV1, []byte, projectstate.Digest) {
	t.Helper()
	activity := activityTestOrdinary(id, time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC))
	activity.Actor.Assurance = types.AssurancePublicKeyContinuity
	raw, err := projectstate.CanonicalActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := projectstate.DigestActivity(activity)
	if err != nil {
		t.Fatal(err)
	}
	return activity, raw, digest
}

func activityMCPResult(t *testing.T, value any, isError bool) json.RawMessage {
	t.Helper()
	inner, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(activityToolResult{
		Content: []activityToolContent{{Type: "text", Text: string(inner)}},
		IsError: isError,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func activityMCPTextResult(t *testing.T, text string, isError bool) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(activityToolResult{
		Content: []activityToolContent{{Type: "text", Text: text}},
		IsError: isError,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func activityMCPPullFixture(t *testing.T) (ActivityPullV1Result, ActivityPullRequest) {
	t.Helper()
	current := activityTestPolicy(3)
	_, currentDigest := activityTestPolicyEvidence(t, current)
	historical := activityTestPolicy(2)
	_, historicalDigest := activityTestPolicyEvidence(t, historical)
	activity, _, activityDigest := activityMCPTestActivity(t, activityTestIDOne)
	receipt := projectstate.ActivityReceiptV1{
		SchemaVersion: 1, ActivityID: activity.ID, ActivityDigest: activityDigest, Sequence: 5,
		PolicyVersion: historical.PolicyVersion, PolicyDigest: historicalDigest,
		AcceptedAt: time.Date(2026, 8, 30, 13, 4, 0, 0, time.UTC),
	}
	return ActivityPullV1Result{
		Version: 1, EffectivePolicy: current, PolicyDigest: currentDigest,
		HistoricalPolicies: []ActivityPolicyEvidenceV1{{Policy: historical, PolicyDigest: historicalDigest}},
		Deliveries: []ActivityDeliveryV1{{
			SourceRef: "b0000000-0000-4000-8000-000000000001", Activity: activity, ActivityDigest: activityDigest, Receipt: receipt,
		}},
		NextSequence: 5,
	}, ActivityPullRequest{AttachmentRef: "50000000-0000-4000-8000-000000000001", AfterSequence: 3, Limit: 2}
}

func activityMCPPolicyDigest(t *testing.T, policy projectstate.EffectiveActivityPolicyV1) projectstate.Digest {
	t.Helper()
	_, digest := activityTestPolicyEvidence(t, policy)
	return digest
}

func assertActivityMCPCall(t *testing.T, caller *recordingActivityCaller, operation string, arguments any) {
	t.Helper()
	want, err := projectstate.CanonicalJSON(arguments)
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSuffix(want, []byte{'\n'})
	if caller.tool != operation || !bytes.Equal(caller.arguments, want) {
		t.Fatalf("call=(%q,%s), want=(%q,%s)", caller.tool, caller.arguments, operation, want)
	}
}
