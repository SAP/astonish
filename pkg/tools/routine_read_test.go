package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutineReadHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://127.0.0.1:1/escape", 302)
			return
		}
		fmt.Fprint(w, "weather observed")
	}))
	defer server.Close()
	origin, _ := RoutineReadOrigin(server.URL)
	allowed := true
	p := RoutineReadPolicy{Authorize: func(_ context.Context, o string) error {
		if !allowed || o != origin {
			return fmt.Errorf("denied")
		}
		return nil
	}, AllowIP: func(_ context.Context, _ string, ip net.IP) error {
		if !ip.IsLoopback() {
			return fmt.Errorf("denied IP")
		}
		return nil
	}}
	ctx := WithRoutineReadPolicy(context.Background(), p)
	for _, m := range []string{"GET", "HEAD"} {
		r, e := httpRequest(ctx, HttpRequestArgs{URL: server.URL, Method: m})
		if e != nil || r.StatusCode != 200 {
			t.Fatalf("%+v %v", r, e)
		}
	}
	for _, a := range []HttpRequestArgs{{URL: server.URL, Method: "POST"}, {URL: server.URL, Credential: "secret"}, {URL: server.URL, Headers: map[string]string{"Authorization": "secret"}}, {URL: server.URL, BodyPath: "/secret"}, {URL: server.URL, Body: "secret"}, {URL: server.URL + "/redirect"}} {
		if _, e := httpRequest(ctx, a); e == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
	if calls != 3 {
		t.Fatalf("unexpected network effects %d", calls)
	}
	allowed = false
	if _, e := httpRequest(ctx, HttpRequestArgs{URL: server.URL}); e == nil {
		t.Fatal("revoked policy accepted")
	}
	p.AllowIP = nil
	p.Authorize = func(context.Context, string) error { return nil }
	if _, e := httpRequest(WithRoutineReadPolicy(context.Background(), p), HttpRequestArgs{URL: server.URL}); e == nil {
		t.Fatal("default SSRF accepted loopback")
	}
}
func TestRoutineReadOrigin(t *testing.T) {
	for _, raw := range []string{"file:///secret", "http://user:secret@example.com", "https://example.com/#secret", "https://example.com/{{CREDENTIAL:x:y}}"} {
		if _, e := RoutineReadOrigin(raw); e == nil {
			t.Fatal(raw)
		}
	}
	a, _ := RoutineReadOrigin("https://EXAMPLE.com/a")
	if a != "https://example.com:443" {
		t.Fatal(a)
	}
}
