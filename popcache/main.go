package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/yzp0n/ncdn/httprps"
	"github.com/yzp0n/ncdn/types"
)

var originURLStr = flag.String("originURL", "http://localhost:8888", "Origin server URL")
var listenAddr = flag.String("listenAddr", ":8889", "Address to listen on")
var http3ListenAddr = flag.String("http3ListenAddr", "", "Address to listen for HTTP/3; empty disables HTTP/3")
var http3CertFile = flag.String("http3CertFile", "", "TLS certificate for HTTP/3")
var http3KeyFile = flag.String("http3KeyFile", "", "TLS private key for HTTP/3")
var nodeId = flag.String("nodeId", "unknown_node", "Name of the node")
var cacheTTL = flag.Duration("cacheTTL", 30*time.Second, "Cache entry TTL")
var cacheMaxBytes = flag.Int64("cacheMaxBytes", 64<<20, "Maximum cache size in bytes")
var cacheMaxObjectBytes = flag.Int64("cacheMaxObjectBytes", 8<<20, "Maximum cached object size in bytes")

func main() {
	flag.Parse()

	originURL, err := url.Parse(*originURLStr)
	if err != nil {
		log.Fatalf("Failed to parse origin URL %q: %v", *originURLStr, err)
	}

	start := time.Now()
	cache, err := newMemoryCache(*cacheMaxBytes, *cacheMaxObjectBytes)
	if err != nil {
		log.Fatalf("Invalid cache size: %v", err)
	}

	mux := http.NewServeMux()
	rps := httprps.NewMiddleware(mux)
	http.Handle("/", rps)

	mux.HandleFunc("/statusz", func(w http.ResponseWriter, r *http.Request) {
		s := struct {
			types.PoPStatus
			Cache cacheStats `json:"cache"`
		}{
			PoPStatus: types.PoPStatus{
				Id:     *nodeId,
				Uptime: time.Since(start).Seconds(),
				Load:   rps.GetRPS(),
			},
			Cache: cache.stats(),
		}
		bs, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			log.Printf("Failed to marshal PoP status: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write(bs)
	})
	mux.HandleFunc("/latencyz", func(w http.ResponseWriter, r *http.Request) {
		// return 204
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", &httputil.ReverseProxy{
		Transport: newCachingTransport(nil, cache, *cacheTTL),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetXForwarded()
			r.Out.Header.Set("X-NCDN-PoPCache-NodeId", *nodeId)
			r.SetURL(originURL)
		},
	})
	if *http3ListenAddr != "" {
		if *http3CertFile == "" || *http3KeyFile == "" {
			log.Fatal("-http3CertFile and -http3KeyFile are required when HTTP/3 is enabled")
		}
		go serveHTTP3(*http3ListenAddr, *http3CertFile, *http3KeyFile, mux)
	}

	log.Printf("Listening on %s...", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, nil); err != nil {
		log.Fatal(err)
	}
}
