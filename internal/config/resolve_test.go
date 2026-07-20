package config

import (
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		input    ResolveInput
		required bool
		expected string
		wantErr  bool
	}{
		{
			name:     "flag takes priority",
			input:    ResolveInput{Flag: "from-flag", Local: "from-local", Global: "from-global"},
			required: true,
			expected: "from-flag",
		},
		{
			name:     "local overrides global",
			input:    ResolveInput{Local: "from-local", Global: "from-global"},
			required: true,
			expected: "from-local",
		},
		{
			name:     "global used if no local",
			input:    ResolveInput{Global: "from-global"},
			required: true,
			expected: "from-global",
		},
		{
			name:     "required error if all empty",
			input:    ResolveInput{},
			required: true,
			wantErr:  true,
		},
		{
			name:     "optional returns empty if all empty",
			input:    ResolveInput{},
			required: false,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.input, tt.required)
			if tt.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, got)
			}
		})
	}
}
