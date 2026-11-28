package browser

import "testing"

func TestNavigationGuardAllowLoopback(t *testing.T) {
	guard := DefaultNavigationGuard()
	guard.AllowLoopback = true
	for _, rawURL := range []string{
		"http://localhost:9393/#/settings", "https://LOCALHOST:9393/path?q=1#fragment",
		"http://127.0.0.1:9393", "http://127.0.0.2:9393", "http://[::1]:9393/#/settings",
		NormalizeLoopbackURL("http://localhost:9393/#/settings"),
	} {
		t.Run(rawURL, func(t *testing.T) {
			if err := guard.Check(rawURL); err != nil {
				t.Fatalf("Code loopback navigation denied: %v", err)
			}
			if err := DefaultNavigationGuard().Check(rawURL); err == nil {
				t.Fatal("normal guard must still deny loopback")
			}
		})
	}
}

func TestNavigationGuardAllowLoopbackRetainsSSRFProtection(t *testing.T) {
	guard := DefaultNavigationGuard()
	guard.AllowLoopback = true
	for _, rawURL := range []string{
		"file://localhost/etc/passwd", "ftp://127.0.0.1/file", "javascript:alert(1)",
		"http://10.0.0.1", "http://172.16.0.1", "http://192.168.1.1",
		"http://169.254.169.254/latest/meta-data", "http://[fe80::1]", "http://[fd00::1]",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if err := guard.Check(rawURL); err == nil {
				t.Fatal("Code guard must deny invalid protocol/private non-loopback address")
			}
		})
	}
}
