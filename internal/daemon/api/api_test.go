package api

import "testing"

func TestSyncRequestDependencySetCombinesBothForms(t *testing.T) {
	got := SyncRequest{Dependency: "a", Dependencies: []string{"b", "c"}}.DependencySet()
	if len(got) != 3 {
		t.Errorf("DependencySet = %v, want b, c and the legacy a", got)
	}
	if len((SyncRequest{}).DependencySet()) != 0 {
		t.Error("an empty request must mean everything (empty set)")
	}
}
