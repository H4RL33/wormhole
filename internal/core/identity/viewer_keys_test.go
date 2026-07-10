package identity

import (
	"context"
	"errors"
	"testing"
)

// TestCreateViewerKey_ResolveViewerKey_RoundTrip covers the base case:
// create a viewer key for a project, then resolve it back with no
// projectID constraint.
func TestCreateViewerKey_ResolveViewerKey_RoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "viewer-key-round-trip")

	rawKey, createdID, err := s.CreateViewerKey(ctx, projectID, "test-key")
	if err != nil {
		t.Fatalf("CreateViewerKey: %v", err)
	}

	if rawKey == "" {
		t.Fatal("CreateViewerKey returned empty raw key")
	}
	if createdID == "" {
		t.Fatal("CreateViewerKey returned empty ID")
	}

	resolvedProjectID, err := s.ResolveViewerKey(ctx, "", rawKey)
	if err != nil {
		t.Fatalf("ResolveViewerKey: %v", err)
	}

	if resolvedProjectID != projectID {
		t.Errorf("ResolveViewerKey returned projectID %q, want %q", resolvedProjectID, projectID)
	}
}

// TestResolveViewerKey_WithProjectIDConstraint: resolving with a matching
// projectID succeeds.
func TestResolveViewerKey_WithProjectIDConstraint(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "viewer-key-constraint")

	rawKey, _, err := s.CreateViewerKey(ctx, projectID, "test-key")
	if err != nil {
		t.Fatalf("CreateViewerKey: %v", err)
	}

	resolvedProjectID, err := s.ResolveViewerKey(ctx, projectID, rawKey)
	if err != nil {
		t.Fatalf("ResolveViewerKey with matching projectID: %v", err)
	}

	if resolvedProjectID != projectID {
		t.Errorf("ResolveViewerKey returned projectID %q, want %q", resolvedProjectID, projectID)
	}
}

// TestResolveViewerKey_CrossProjectRejected: T3 isolation test — a key issued
// for project A must be rejected when resolved against project B's scope.
func TestResolveViewerKey_CrossProjectRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectA := createProject(t, s, "viewer-key-project-a")
	projectB := createProject(t, s, "viewer-key-project-b")

	rawKey, _, err := s.CreateViewerKey(ctx, projectA, "test-key")
	if err != nil {
		t.Fatalf("CreateViewerKey: %v", err)
	}

	_, err = s.ResolveViewerKey(ctx, projectB, rawKey)
	if !errors.Is(err, ErrInvalidViewerKey) {
		t.Errorf("ResolveViewerKey(projectB, keyFromA) error = %v, want ErrInvalidViewerKey", err)
	}
}

// TestResolveViewerKey_UnknownKeyRejected: an unknown/garbage raw key must
// be rejected.
func TestResolveViewerKey_UnknownKeyRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "viewer-key-unknown")

	_, err := s.ResolveViewerKey(ctx, projectID, "0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrInvalidViewerKey) {
		t.Errorf("ResolveViewerKey(unknown key) error = %v, want ErrInvalidViewerKey", err)
	}
}

// TestResolveViewerKey_EmptyKeyRejected: an empty raw key must be rejected.
func TestResolveViewerKey_EmptyKeyRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	projectID := createProject(t, s, "viewer-key-empty")

	_, err := s.ResolveViewerKey(ctx, projectID, "")
	if !errors.Is(err, ErrInvalidViewerKey) {
		t.Errorf("ResolveViewerKey(\"\") error = %v, want ErrInvalidViewerKey", err)
	}
}
