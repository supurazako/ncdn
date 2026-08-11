package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

var testEdges = []edge{
	{ID: "c0", URL: "https://example.test:4443"},
	{ID: "c1", URL: "https://example.test:4444"},
}

func TestRoundRobinAlternates(t *testing.T) {
	router, err := newRouter(testEdges)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"c0", "c1", "c0", "c1"}
	for index, edgeID := range want {
		selected, err := router.selectEdge("/demo", "round-robin")
		if err != nil {
			t.Fatal(err)
		}
		if selected.ID != edgeID {
			t.Fatalf("selection %d: got %s, want %s", index, selected.ID, edgeID)
		}
	}
}

func TestRendezvousIsStable(t *testing.T) {
	first := rendezvous("/demo", testEdges)
	for range 100 {
		if selected := rendezvous("/demo", testEdges); selected != first {
			t.Fatalf("got %+v, want stable selection %+v", selected, first)
		}
	}
}

func TestRendezvousDistributesNamespaces(t *testing.T) {
	seen := make(map[string]bool)
	for _, namespace := range []string{"/demo", "/news", "/sport", "/music", "/camera/1", "/camera/2"} {
		seen[rendezvous(namespace, testEdges).ID] = true
	}
	if len(seen) != len(testEdges) {
		t.Fatalf("got selections for %d edges, want %d", len(seen), len(testEdges))
	}
}

func TestRouteHandler(t *testing.T) {
	router, err := newRouter(testEdges)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/route?namespace=/demo&strategy=rendezvous", nil)
	response := httptest.NewRecorder()
	router.routeHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", response.Code, response.Body.String())
	}

	var route routeResponse
	if err := json.NewDecoder(response.Body).Decode(&route); err != nil {
		t.Fatal(err)
	}
	if route.Namespace != "/demo" || route.Strategy != "rendezvous" || route.Edge == "" || route.URL == "" {
		t.Fatalf("unexpected response: %+v", route)
	}
}

func TestRouteRequiresNamespace(t *testing.T) {
	router, err := newRouter(testEdges)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/route", nil)
	response := httptest.NewRecorder()
	router.routeHandler(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestAlgorithmComparison(t *testing.T) {
	roundRobin := compareStrategy("round-robin", testEdges, 1000, 10)
	rendezvous := compareStrategy("rendezvous", testEdges, 1000, 10)

	if roundRobin.ImbalancePercent != 0 {
		t.Fatalf("round robin imbalance: got %.2f%%, want 0", roundRobin.ImbalancePercent)
	}
	if roundRobin.AverageEdgesPerNamespace != 2 {
		t.Fatalf("round robin edges per namespace: got %.2f, want 2", roundRobin.AverageEdgesPerNamespace)
	}
	if rendezvous.AverageEdgesPerNamespace != 1 {
		t.Fatalf("rendezvous edges per namespace: got %.2f, want 1", rendezvous.AverageEdgesPerNamespace)
	}
	if rendezvous.RemappedAfterAddPercent >= roundRobin.RemappedAfterAddPercent {
		t.Fatalf(
			"rendezvous remapping %.2f%% should be lower than round robin %.2f%%",
			rendezvous.RemappedAfterAddPercent,
			roundRobin.RemappedAfterAddPercent,
		)
	}
	if rendezvous.RemappedAfterAddPercent < 28 || rendezvous.RemappedAfterAddPercent > 38 {
		t.Fatalf("rendezvous remapping: got %.2f%%, want approximately 33%%", rendezvous.RemappedAfterAddPercent)
	}
}

func TestCompareHandler(t *testing.T) {
	router, err := newRouter(testEdges)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/compare?namespaces=100&requests_per_namespace=4", nil)
	response := httptest.NewRecorder()
	router.compareHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", response.Code, response.Body.String())
	}

	var comparison comparisonResponse
	if err := json.NewDecoder(response.Body).Decode(&comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.Namespaces != 100 || comparison.RequestsPerNamespace != 4 || len(comparison.Results) != 2 {
		t.Fatalf("unexpected response: %+v", comparison)
	}
	if len(comparison.LoadExperiment.Results) != 3 {
		t.Fatalf("got %d load results, want 3", len(comparison.LoadExperiment.Results))
	}
}

func TestBoundedRendezvousTradesAffinityForLoadBound(t *testing.T) {
	experiment := compareLoadStrategies(testEdges, 1000, 10, 5000, 125)
	plain := experiment.Results[1]
	bounded := experiment.Results[2]

	if bounded.MaximumLoadPercentOfMean > 125.1 {
		t.Fatalf("bounded maximum load: got %.2f%%, want at most 125%%", bounded.MaximumLoadPercentOfMean)
	}
	if bounded.MaximumLoadPercentOfMean >= plain.MaximumLoadPercentOfMean {
		t.Fatalf("bounded load %.2f%% should be lower than rendezvous %.2f%%", bounded.MaximumLoadPercentOfMean, plain.MaximumLoadPercentOfMean)
	}
	if bounded.UpstreamSubscriptions <= plain.UpstreamSubscriptions {
		t.Fatalf(
			"bounded upstream subscriptions %d should exceed rendezvous %d",
			bounded.UpstreamSubscriptions,
			plain.UpstreamSubscriptions,
		)
	}
}
