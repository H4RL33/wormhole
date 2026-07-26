package index

import (
	"errors"
	"testing"
)

func TestParseTrackedStageRejectsTraversalCollisionAndNonStageZero(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "traversal", raw: "100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 0\t../escape.go\x00"},
		{name: "absolute", raw: "100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 0\t/escape.go\x00"},
		{name: "duplicate", raw: "100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 0\ta.go\x00100644 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 0\ta.go\x00"},
		{name: "conflict", raw: "100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2\ta.go\x00"},
		{name: "symlink", raw: "120000 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 0\ta.go\x00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseTrackedStage([]byte(tt.raw)); !errors.Is(err, ErrUnsupportedTrackedFile) {
				t.Fatalf("parseTrackedStage() error = %v, want ErrUnsupportedTrackedFile", err)
			}
		})
	}
}
