package main

import (
	"log"
	"net/http"

	"github.com/quic-go/quic-go/http3"
)

func serveHTTP3(addr, certFile, keyFile string, handler http.Handler) {
	server := http3.Server{
		Addr:    addr,
		Handler: handler,
	}
	if err := server.ListenAndServeTLS(certFile, keyFile); err != nil {
		log.Printf("HTTP/3 server stopped: %v", err)
	}
}
