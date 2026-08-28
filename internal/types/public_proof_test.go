package types

import (
	"encoding/json"
	"testing"
)

func TestPublicRequestProofWireOrderAndOptionalSession(t *testing.T) {
	proof := PublicRequestProof{
		KeyID: "sha256:key", PublicKey: "public", Timestamp: "2026-08-28T12:00:00Z",
		Nonce: "nonce", Signature: "signature",
	}
	got, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"key_id":"sha256:key","public_key":"public","timestamp":"2026-08-28T12:00:00Z","nonce":"nonce","signature":"signature"}`
	if string(got) != want {
		t.Fatalf("proof JSON = %s, want %s", got, want)
	}
	proof.SessionID = "session-1"
	got, err = json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"key_id":"sha256:key","public_key":"public","timestamp":"2026-08-28T12:00:00Z","nonce":"nonce","signature":"signature","session_id":"session-1"}`
	if string(got) != want {
		t.Fatalf("session proof JSON = %s, want %s", got, want)
	}
}
