package api

import (
	"net/http"
	"testing"
	"time"
)

func TestIsWebSocketUpgrade(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{
			name:    "standard WebSocket upgrade",
			headers: map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"},
			want:    true,
		},
		{
			name:    "case insensitive connection",
			headers: map[string]string{"Connection": "upgrade", "Upgrade": "websocket"},
			want:    true,
		},
		{
			name:    "case insensitive upgrade",
			headers: map[string]string{"Connection": "Upgrade", "Upgrade": "WebSocket"},
			want:    true,
		},
		{
			name:    "multi-value connection header",
			headers: map[string]string{"Connection": "keep-alive, Upgrade", "Upgrade": "websocket"},
			want:    true,
		},
		{
			name:    "no connection header",
			headers: map[string]string{"Upgrade": "websocket"},
			want:    false,
		},
		{
			name:    "no upgrade header",
			headers: map[string]string{"Connection": "Upgrade"},
			want:    false,
		},
		{
			name:    "wrong upgrade protocol",
			headers: map[string]string{"Connection": "Upgrade", "Upgrade": "h2c"},
			want:    false,
		},
		{
			name:    "empty headers",
			headers: map[string]string{},
			want:    false,
		},
		{
			name:    "connection without upgrade token",
			headers: map[string]string{"Connection": "keep-alive", "Upgrade": "websocket"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "http://localhost/ws", nil)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			if got := isWebSocketUpgrade(r); got != tt.want {
				t.Errorf("isWebSocketUpgrade() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIPCache(t *testing.T) {
	// Clean the cache
	ipCache.Range(func(key, _ any) bool {
		ipCache.Delete(key)
		return true
	})

	containerName := "test-container-xyz"

	// Store an entry manually
	ipCache.Store(containerName, &ipCacheEntry{
		ip:     "10.100.0.5",
		expiry: time.Now().Add(30 * time.Second),
	})

	// Retrieve it
	if entry, ok := ipCache.Load(containerName); !ok {
		t.Error("expected to find cached IP")
	} else {
		cached := entry.(*ipCacheEntry)
		if cached.ip != "10.100.0.5" {
			t.Errorf("cached IP = %q, want 10.100.0.5", cached.ip)
		}
	}

	// Invalidate it
	InvalidateIPCache(containerName)

	if _, ok := ipCache.Load(containerName); ok {
		t.Error("expected cache entry to be invalidated")
	}
}

func TestIPCacheExpiry(t *testing.T) {
	// Clean the cache
	ipCache.Range(func(key, _ any) bool {
		ipCache.Delete(key)
		return true
	})

	containerName := "test-container-expired"

	// Store an entry that is already expired
	ipCache.Store(containerName, &ipCacheEntry{
		ip:     "10.100.0.99",
		expiry: time.Now().Add(-1 * time.Second),
	})

	if entry, ok := ipCache.Load(containerName); ok {
		cached := entry.(*ipCacheEntry)
		if time.Now().After(cached.expiry) {
			ipCache.Delete(containerName)
		}
	}

	if _, ok := ipCache.Load(containerName); ok {
		t.Error("expected expired cache entry to be cleaned up")
	}
}

func TestPortProxyManagerSingleton(t *testing.T) {
	mgr1 := GetPortProxyManager()
	mgr2 := GetPortProxyManager()
	if mgr1 != mgr2 {
		t.Error("GetPortProxyManager should return the same instance")
	}
}

func TestPortProxyManagerGetHostPortNotRunning(t *testing.T) {
	mgr := GetPortProxyManager()
	// Port that was never started should return 0
	hp := mgr.GetHostPort("nonexistent-container", 3000)
	if hp != 0 {
		t.Errorf("GetHostPort for non-running proxy returned %d, want 0", hp)
	}
}

func TestPortProxyManagerListForContainer(t *testing.T) {
	mgr := GetPortProxyManager()
	// Container with no active proxies should return empty map
	result := mgr.ListForContainer("nonexistent-container")
	if len(result) != 0 {
		t.Errorf("ListForContainer for unknown container returned %d entries, want 0", len(result))
	}
}

func TestPortProxyManagerStopNonexistent(t *testing.T) {
	mgr := GetPortProxyManager()
	stopped := mgr.StopProxy("nonexistent-container", 3000)
	if stopped {
		t.Error("StopProxy should return false for non-running proxy")
	}
}

func TestPortProxyManagerStopAllNonexistent(t *testing.T) {
	mgr := GetPortProxyManager()
	count := mgr.StopAllForContainer("nonexistent-container")
	if count != 0 {
		t.Errorf("StopAllForContainer returned %d, want 0", count)
	}
}

func TestProxyKey(t *testing.T) {
	key := proxyKey("astn-sess-abc123", 3000)
	if key != "astn-sess-abc123:3000" {
		t.Errorf("proxyKey = %q, want %q", key, "astn-sess-abc123:3000")
	}
}

// --- SubdomainRouter tests ---

func TestSubdomainRouterSingleton(t *testing.T) {
	sr1 := GetSubdomainRouter()
	sr2 := GetSubdomainRouter()
	if sr1 != sr2 {
		t.Error("GetSubdomainRouter should return the same instance")
	}
}

func TestSubdomainHostname(t *testing.T) {
	got := SubdomainHostname("astn-sess-abc123", 3000, "astonish.local.muxpie.com")
	want := "astn-sess-abc123-3000.astonish.local.muxpie.com"
	if got != want {
		t.Errorf("SubdomainHostname = %q, want %q", got, want)
	}
}

func TestSubdomainRouterRegisterAndLookup(t *testing.T) {
	sr := GetSubdomainRouter()
	hostname := "test-container-9999.example.com"

	sr.RegisterHost(hostname, "test-container", "test-session", 9999)

	cn, sid, port, ok := sr.Lookup(hostname)
	if !ok {
		t.Fatal("expected Lookup to find registered host")
	}
	if cn != "test-container" || sid != "test-session" || port != 9999 {
		t.Errorf("Lookup = (%q, %q, %d), want (%q, %q, %d)", cn, sid, port, "test-container", "test-session", 9999)
	}

	cn, sid, port, ok = sr.Lookup(hostname + ":9393")
	if !ok {
		t.Fatal("expected Lookup to find host with port suffix")
	}
	if cn != "test-container" || sid != "test-session" || port != 9999 {
		t.Errorf("Lookup with port = (%q, %q, %d), want (%q, %q, %d)", cn, sid, port, "test-container", "test-session", 9999)
	}

	_, _, _, ok = sr.Lookup("unknown.example.com")
	if ok {
		t.Error("expected Lookup to return false for unknown host")
	}

	sr.UnregisterHost(hostname)
}

func TestSubdomainRouterUnregister(t *testing.T) {
	sr := GetSubdomainRouter()
	hostname := "unreg-test-3000.example.com"

	sr.RegisterHost(hostname, "unreg-test", "unreg-session", 3000)
	sr.UnregisterHost(hostname)

	_, _, _, ok := sr.Lookup(hostname)
	if ok {
		t.Error("expected Lookup to return false after UnregisterHost")
	}
}

func TestSubdomainRouterUnregisterAllForContainer(t *testing.T) {
	sr := GetSubdomainRouter()

	sr.RegisterHost("multi-test-3000.example.com", "multi-test", "multi-session", 3000)
	sr.RegisterHost("multi-test-8080.example.com", "multi-test", "multi-session", 8080)
	sr.RegisterHost("other-container-3000.example.com", "other-container", "other-session", 3000)

	count := sr.UnregisterAllForContainer("multi-test")
	if count != 2 {
		t.Errorf("UnregisterAllForContainer returned %d, want 2", count)
	}

	_, _, _, ok := sr.Lookup("multi-test-3000.example.com")
	if ok {
		t.Error("expected multi-test-3000 to be unregistered")
	}

	// Other container should still be registered
	_, _, _, ok = sr.Lookup("other-container-3000.example.com")
	if !ok {
		t.Error("expected other-container-3000 to still be registered")
	}

	// Clean up
	sr.UnregisterHost("other-container-3000.example.com")
}

func TestSubdomainRouterListForContainer(t *testing.T) {
	sr := GetSubdomainRouter()

	sr.RegisterHost("list-test-3000.example.com", "list-test", "list-session", 3000)
	sr.RegisterHost("list-test-8080.example.com", "list-test", "list-session", 8080)

	result := sr.ListForContainer("list-test")
	if len(result) != 2 {
		t.Errorf("ListForContainer returned %d entries, want 2", len(result))
	}
	if result[3000] != "list-test-3000.example.com" {
		t.Errorf("ListForContainer[3000] = %q, want %q", result[3000], "list-test-3000.example.com")
	}
	if result[8080] != "list-test-8080.example.com" {
		t.Errorf("ListForContainer[8080] = %q, want %q", result[8080], "list-test-8080.example.com")
	}

	// Unknown container
	result = sr.ListForContainer("nonexistent")
	if len(result) != 0 {
		t.Errorf("ListForContainer for unknown returned %d entries, want 0", len(result))
	}

	// Clean up
	sr.UnregisterAllForContainer("list-test")
}
