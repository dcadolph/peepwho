// Command fakejira serves a fake Jira Server site on the loopback address for the Mac host's
// token check. It answers only requests carrying the bearer token it was started with, so the
// host's refresh succeeds with the right Keychain token and fails with a missing or wrong one.
// It prints its URL on stdout and serves until killed.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"github.com/dcadolph/peepwho/internal/fakeapi"
)

// main starts the fake site behind the bearer check and serves until killed.
func main() {
	addr := flag.String("addr", "127.0.0.1:0", "Address to listen on.")
	token := flag.String("token", "good", "Bearer token the site accepts.")
	flag.Parse()
	if err := serve(*addr, *token); err != nil {
		log.Fatal(err)
	}
}

// serve listens on addr and proxies every request carrying token to a fake Jira Server site
// holding one KAFKA issue. Any other request gets 401.
func serve(addr, token string) error {
	fake := &fakeapi.Jira{ServerMode: true, Issues: []fakeapi.JiraIssue{{
		Key: "KAFKA-1", Summary: "consumer group rebalance", Description: "session timeout",
		AssigneeEmail: "dana@example.invalid", ProjectKey: "KAFKA", ProjectName: "Kafka",
		Updated: "2026-06-20T09:30:00.000+0000",
	}}}
	inner := fake.Server()
	defer inner.Close()
	target, err := url.Parse(inner.URL)
	if err != nil {
		return fmt.Errorf("fakejira: parse inner url: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("fakejira: listen: %w", err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "http://%s\n", ln.Addr()); err != nil {
		return fmt.Errorf("fakejira: print url: %w", err)
	}
	want := "Bearer " + token
	return http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
}
