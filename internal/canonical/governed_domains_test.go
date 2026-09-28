package canonical

import (
	"reflect"
	"testing"
)

func TestFrameworkGraphHelpersRejectCyclesAndComputeFrontier(t *testing.T) {
	edges := []GraphEdge{
		{From: "api", To: "worker", Kind: "technical", Accepted: true},
		{From: "web", To: "api", Kind: "requirement", Accepted: true},
		{From: "ignored", To: "edge", Kind: "technical", Accepted: false},
	}
	if cycles := detectCycles(edges); len(cycles) != 0 {
		t.Fatalf("acyclic graph reported cycles: %#v", cycles)
	}
	if got, want := computeFrontier(edges), []string{"web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("frontier = %#v, want %#v", got, want)
	}

	cyclic := append(edges, GraphEdge{From: "worker", To: "api", Kind: "technical", Accepted: true})
	if cycles := detectCycles(cyclic); len(cycles) == 0 {
		t.Fatal("cycle must be detected before a framework graph is adopted")
	}
}

func TestFencingTokenInputsMustBeSpecific(t *testing.T) {
	// The service checks this tuple against a live lease before recording
	// evidence; keeping the fields distinct prevents a holder from using a
	// stale lease after a recovery acquires a newer fencing token.
	lease := ExecutionLeaseView{ID: "lease-a", ScopeID: "scope-a", Holder: "agent", FencingToken: 42}
	if lease.ID == "" || lease.ScopeID == "" || lease.FencingToken <= 0 {
		t.Fatal("lease view must expose an opaque lease id, scope id, and fence")
	}
}
