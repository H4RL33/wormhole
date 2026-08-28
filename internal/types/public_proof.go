package types

type PublicRequestProof struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Timestamp string `json:"timestamp"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
	SessionID string `json:"session_id,omitempty"`
}

type PublicRequestSignature struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}
