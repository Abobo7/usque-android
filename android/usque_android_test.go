package usqueandroid

import "testing"

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		port int
	}{
		{name: "ipv4 default port", raw: "162.159.198.2", want: "162.159.198.2", port: 443},
		{name: "ipv4 custom port", raw: "162.159.198.2:1701", want: "162.159.198.2", port: 1701},
		{name: "ipv6 custom port", raw: "[2606:4700:103::]:1701", want: "2606:4700:103::", port: 1701},
		{name: "ipv6 default port", raw: "2606:4700:103::", want: "2606:4700:103::", port: 443},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEndpoint(tt.raw)
			if err != nil {
				t.Fatalf("parseEndpoint(%q): %v", tt.raw, err)
			}
			if got.IP.String() != tt.want || got.Port != tt.port {
				t.Fatalf("parseEndpoint(%q) = %s, want %s:%d", tt.raw, got, tt.want, tt.port)
			}
		})
	}
}

func TestParseEndpointRejectsInvalidPort(t *testing.T) {
	if _, err := parseEndpoint("162.159.198.2:0"); err == nil {
		t.Fatal("expected port 0 to be rejected")
	}
	if _, err := parseEndpoint("[2606:4700:103::]:70000"); err == nil {
		t.Fatal("expected an out-of-range port to be rejected")
	}
}

func TestNormalizePeerEndpoint(t *testing.T) {
	got, err := normalizePeerEndpoint("[2606:4700:103::]:0", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2606:4700:103::" {
		t.Fatalf("got %q", got)
	}

	if got, err := normalizePeerEndpoint("", true); err != nil || got != "" {
		t.Fatalf("optional empty endpoint = %q, %v", got, err)
	}
}
