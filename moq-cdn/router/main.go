package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type edge struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type router struct {
	edges      []edge
	roundRobin atomic.Uint64
	mu         sync.Mutex
	selections map[string]map[string]uint64
}

type routeResponse struct {
	Namespace string `json:"namespace"`
	Strategy  string `json:"strategy"`
	Edge      string `json:"edge"`
	URL       string `json:"url"`
}

type comparisonResult struct {
	Strategy                 string         `json:"strategy"`
	Selections               map[string]int `json:"selections"`
	ImbalancePercent         float64        `json:"imbalance_percent"`
	AverageEdgesPerNamespace float64        `json:"average_edges_per_namespace"`
	RemappedAfterAddPercent  float64        `json:"remapped_after_add_percent"`
}

type comparisonResponse struct {
	Namespaces           int                `json:"namespaces"`
	RequestsPerNamespace int                `json:"requests_per_namespace"`
	Results              []comparisonResult `json:"results"`
}

func newRouter(edges []edge) (*router, error) {
	if len(edges) == 0 {
		return nil, errors.New("at least one edge is required")
	}

	seen := make(map[string]struct{}, len(edges))
	for _, candidate := range edges {
		if candidate.ID == "" || candidate.URL == "" {
			return nil, errors.New("edge ID and URL are required")
		}
		if _, ok := seen[candidate.ID]; ok {
			return nil, fmt.Errorf("duplicate edge ID %q", candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
	}

	return &router{
		edges:      append([]edge(nil), edges...),
		selections: make(map[string]map[string]uint64),
	}, nil
}

func (r *router) selectEdge(namespace, strategy string) (edge, error) {
	var selected edge
	switch strategy {
	case "round-robin":
		index := r.roundRobin.Add(1) - 1
		selected = r.edges[index%uint64(len(r.edges))]
	case "rendezvous":
		selected = rendezvous(namespace, r.edges)
	default:
		return edge{}, fmt.Errorf("unsupported strategy %q", strategy)
	}

	r.mu.Lock()
	if r.selections[strategy] == nil {
		r.selections[strategy] = make(map[string]uint64)
	}
	r.selections[strategy][selected.ID]++
	r.mu.Unlock()

	return selected, nil
}

func rendezvous(key string, edges []edge) edge {
	selected := edges[0]
	selectedScore := rendezvousScore(key, selected.ID)
	for _, candidate := range edges[1:] {
		score := rendezvousScore(key, candidate.ID)
		if score > selectedScore || score == selectedScore && candidate.ID < selected.ID {
			selected = candidate
			selectedScore = score
		}
	}
	return selected
}

func rendezvousScore(key, edgeID string) uint64 {
	digest := sha256.Sum256([]byte(key + "\x00" + edgeID))
	return binary.BigEndian.Uint64(digest[:8])
}

func (r *router) routeHandler(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	namespace := request.URL.Query().Get("namespace")
	if namespace == "" {
		http.Error(w, "namespace is required", http.StatusBadRequest)
		return
	}
	strategy := request.URL.Query().Get("strategy")
	if strategy == "" {
		strategy = "rendezvous"
	}

	selected, err := r.selectEdge(namespace, strategy)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(routeResponse{
		Namespace: namespace,
		Strategy:  strategy,
		Edge:      selected.ID,
		URL:       selected.URL,
	})
}

func (r *router) metricsHandler(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.mu.Lock()
	snapshot := make(map[string]map[string]uint64, len(r.selections))
	for strategy, byEdge := range r.selections {
		snapshot[strategy] = make(map[string]uint64, len(byEdge))
		for edgeID, count := range byEdge {
			snapshot[strategy][edgeID] = count
		}
	}
	r.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(w, "# HELP moq_router_selections_total Number of routing decisions.")
	fmt.Fprintln(w, "# TYPE moq_router_selections_total counter")
	strategies := make([]string, 0, len(snapshot))
	for strategy := range snapshot {
		strategies = append(strategies, strategy)
	}
	sort.Strings(strategies)
	for _, strategy := range strategies {
		edgeIDs := make([]string, 0, len(snapshot[strategy]))
		for edgeID := range snapshot[strategy] {
			edgeIDs = append(edgeIDs, edgeID)
		}
		sort.Strings(edgeIDs)
		for _, edgeID := range edgeIDs {
			fmt.Fprintf(w, "moq_router_selections_total{strategy=%q,edge=%q} %d\n", strategy, edgeID, snapshot[strategy][edgeID])
		}
	}
}

func (r *router) compareHandler(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	namespaces, err := positiveIntParameter(request, "namespaces", 1000, 100000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	requestsPerNamespace, err := positiveIntParameter(request, "requests_per_namespace", 10, 1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	response := comparisonResponse{
		Namespaces:           namespaces,
		RequestsPerNamespace: requestsPerNamespace,
		Results: []comparisonResult{
			compareStrategy("round-robin", r.edges, namespaces, requestsPerNamespace),
			compareStrategy("rendezvous", r.edges, namespaces, requestsPerNamespace),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func positiveIntParameter(request *http.Request, name string, fallback, maximum int) (int, error) {
	raw := request.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", name, maximum)
	}
	return value, nil
}

func compareStrategy(strategy string, edges []edge, namespaceCount, requestsPerNamespace int) comparisonResult {
	selections := make(map[string]int, len(edges))
	for _, candidate := range edges {
		selections[candidate.ID] = 0
	}

	edgesPerNamespace := 0
	roundRobinIndex := 0
	for namespaceIndex := range namespaceCount {
		namespace := fmt.Sprintf("/experiment/%d", namespaceIndex)
		seen := make(map[string]struct{}, len(edges))
		for range requestsPerNamespace {
			selected := simulatedSelection(strategy, namespace, edges, roundRobinIndex)
			roundRobinIndex++
			selections[selected.ID]++
			seen[selected.ID] = struct{}{}
		}
		edgesPerNamespace += len(seen)
	}

	addedEdges := append(append([]edge(nil), edges...), edge{ID: "added-edge", URL: "https://added.invalid"})
	remapped := 0
	for namespaceIndex := range namespaceCount {
		namespace := fmt.Sprintf("/experiment/%d", namespaceIndex)
		before := simulatedSelection(strategy, namespace, edges, namespaceIndex)
		after := simulatedSelection(strategy, namespace, addedEdges, namespaceIndex)
		if before.ID != after.ID {
			remapped++
		}
	}

	return comparisonResult{
		Strategy:                 strategy,
		Selections:               selections,
		ImbalancePercent:         imbalancePercent(selections),
		AverageEdgesPerNamespace: float64(edgesPerNamespace) / float64(namespaceCount),
		RemappedAfterAddPercent:  float64(remapped) * 100 / float64(namespaceCount),
	}
}

func simulatedSelection(strategy, namespace string, edges []edge, index int) edge {
	if strategy == "round-robin" {
		return edges[index%len(edges)]
	}
	return rendezvous(namespace, edges)
}

func imbalancePercent(selections map[string]int) float64 {
	minimum := -1
	maximum := 0
	total := 0
	for _, count := range selections {
		if minimum == -1 || count < minimum {
			minimum = count
		}
		if count > maximum {
			maximum = count
		}
		total += count
	}
	mean := float64(total) / float64(len(selections))
	if mean == 0 {
		return 0
	}
	return float64(maximum-minimum) * 100 / mean
}

func parseEdges(raw string) ([]edge, error) {
	var edges []edge
	for _, item := range strings.Split(raw, ",") {
		id, url, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			return nil, fmt.Errorf("invalid edge %q; expected id=url", item)
		}
		edges = append(edges, edge{ID: id, URL: url})
	}
	return edges, nil
}

func main() {
	edges, err := parseEdges(envOrDefault("EDGES", "c0=https://127.0.0.1:4443,c1=https://127.0.0.1:4444"))
	if err != nil {
		log.Fatal(err)
	}
	router, err := newRouter(edges)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/route", router.routeHandler)
	mux.HandleFunc("/compare", router.compareHandler)
	mux.HandleFunc("/metrics", router.metricsHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	address := envOrDefault("LISTEN_ADDR", ":8080")
	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("routing API listening on %s with %d edges", address, len(edges))
	log.Fatal(server.ListenAndServe())
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
