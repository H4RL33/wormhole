package identity

import "testing"

func TestHasPermission(t *testing.T) {
	scope := AuthenticatedScope{Permissions: []string{"task.create", "kb.write"}}

	cases := []struct {
		name string
		perm string
		want bool
	}{
		{"granted first", "task.create", true},
		{"granted second", "kb.write", true},
		{"not granted", "task.assign", false},
		{"empty name never matches", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scope.HasPermission(tc.perm); got != tc.want {
				t.Errorf("HasPermission(%q) = %v, want %v", tc.perm, got, tc.want)
			}
		})
	}

	empty := AuthenticatedScope{}
	if empty.HasPermission("task.create") {
		t.Error("empty scope: HasPermission(task.create) = true, want false")
	}
}
