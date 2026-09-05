package endpoint

import "testing"

func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"http://LOCALHOST:05300/", "http://localhost:5300"},
		{"https://example.com:443/proxy/", "https://example.com/proxy"},
		{"http://[::1]:80", "http://[::1]"},
		{"https://example.com/%E5%BC%80%E5%8F%91/", "https://example.com/%E5%BC%80%E5%8F%91"},
		{"https://example.com/a%20b", "https://example.com/a%20b"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := Normalize(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRejectAmbiguousTargets(t *testing.T) {
	for _, input := range []string{
		"", "localhost:5300", "file:///tmp/info", "http://", "http://user:secret@host",
		"http://host?token=secret", "http://host?", "http://host#", "http://host/#fragment",
		"http://host:0", "http://host:65536", "http://host:", "http://host:abc",
		"http://host/a/../b", "http://host/%2e%2e/b", "http://host/a%2Fb", "http://host/a%252fb",
		"http://host/a\\b", "http://host/a%5Cb", "http://host/a%0ab", "http://host\n",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := Normalize(input); err == nil {
				t.Fatal("ambiguous target accepted")
			}
		})
	}
}
