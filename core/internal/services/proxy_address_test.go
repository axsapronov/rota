package services

import "testing"

func TestExtractIP(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"8.8.8.8:8080", "8.8.8.8"},
		{"103.10.60.178:8080:Indonesia", "103.10.60.178"},
		{"user:pass@103.10.60.178:8080:Indonesia", "103.10.60.178"},
		{"0.0.0.0:3128", "0.0.0.0"},
		{"127.0.0.7", "127.0.0.7"},
		{"not-an-ip:8080:Foo", ""},
	}
	for _, tc := range tests {
		got := extractIP(tc.addr)
		if got != tc.want {
			t.Errorf("extractIP(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestNormalizeProxyAddress(t *testing.T) {
	got := normalizeProxyAddress("103.10.60.178:8080:Indonesia")
	if got != "103.10.60.178:8080" {
		t.Fatalf("got %q", got)
	}
}

func TestIsSkippableGeoIP(t *testing.T) {
	if !isSkippableGeoIP("127.0.0.1") {
		t.Fatal("loopback should be skippable")
	}
	if !isSkippableGeoIP("0.0.0.0") {
		t.Fatal("unspecified should be skippable")
	}
	if isSkippableGeoIP("8.8.8.8") {
		t.Fatal("public IP should not be skippable")
	}
}
