package main

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func TestHTTP3RoundTrip(t *testing.T) {
	certificateServer := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificateServer.StartTLS()
	defer certificateServer.Close()
	certificate := certificateServer.TLS.Certificates[0]

	packetConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	server := &http3.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test-Protocol", "h3")
			_, _ = io.WriteString(w, "hello over http/3")
		}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}},
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(packetConn) }()
	defer func() {
		_ = server.Close()
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("HTTP/3 server did not stop")
		}
	}()

	client := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer client.Close()
	request, err := http.NewRequest(http.MethodGet, "https://127.0.0.1:"+portString(packetConn.LocalAddr()), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "hello over http/3" ||
		response.Header.Get("X-Test-Protocol") != "h3" {
		t.Fatalf("unexpected HTTP/3 response: status=%d body=%q protocol=%q", response.StatusCode, body, response.Header.Get("X-Test-Protocol"))
	}
}

func portString(address net.Addr) string {
	_, port, err := net.SplitHostPort(address.String())
	if err != nil {
		panic(err)
	}
	return port
}
