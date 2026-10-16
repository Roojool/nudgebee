package safehttp

import (
	"net"
	"testing"
)

func TestIsRestrictedIP_NAT64(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"NAT64 metadata endpoint", "64:ff9b::a9fe:a9fe", true},
		{"NAT64 loopback", "64:ff9b::7f00:1", true},
		{"NAT64 RFC1918 10.x", "64:ff9b::a00:1", true},
		{"NAT64 public IPv4", "64:ff9b::808:808", false},
		{"local-use NAT64 prefix", "64:ff9b:1::a9fe:a9fe", true},
		{"public IPv6", "2607:f8b0:4004:800::200e", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			if got := IsRestrictedIP(ip); got != tt.want {
				t.Errorf("IsRestrictedIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestIsRestrictedIP_CGNAT(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"CGNAT start", "100.64.0.1", true},
		{"CGNAT mid", "100.100.100.100", true},
		{"CGNAT end", "100.127.255.254", true},
		{"just outside CGNAT", "100.128.0.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			if got := IsRestrictedIP(ip); got != tt.want {
				t.Errorf("IsRestrictedIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestIsRestrictedIP_StandardRanges(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"RFC1918 10.x", "10.0.0.1", true},
		{"RFC1918 172.16.x", "172.16.0.1", true},
		{"RFC1918 192.168.x", "192.168.1.1", true},
		{"link-local metadata", "169.254.169.254", true},
		{"multicast", "224.0.0.1", true},
		{"unspecified", "0.0.0.0", true},
		{"public IP", "8.8.8.8", false},
		{"public IP 2", "1.1.1.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			if got := IsRestrictedIP(ip); got != tt.want {
				t.Errorf("IsRestrictedIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestSetAllowedCIDRs(t *testing.T) {
	t.Cleanup(func() { _, _ = SetAllowedCIDRs("") })
	applied, err := SetAllowedCIDRs(" 10.28.0.0/16 , 0.0.0.0/0,")
	if err != nil {
		t.Fatalf("SetAllowedCIDRs: %v", err)
	}
	if len(applied) != 2 {
		t.Fatalf("applied = %v, want 2 entries", applied)
	}

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"allowed private host", "10.28.92.7", false},
		{"other private host covered by 0.0.0.0/0", "192.168.1.1", false},
		{"IPv4-mapped allowed host", "::ffff:10.28.92.7", false},
		{"metadata stays blocked", "169.254.169.254", true},
		{"unspecified stays blocked", "0.0.0.0", true},
		{"multicast stays blocked", "224.0.0.1", true},
		{"NAT64 metadata stays blocked", "64:ff9b::a9fe:a9fe", true},
		{"public stays allowed", "8.8.8.8", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRestrictedIP(net.ParseIP(tt.ip)); got != tt.want {
				t.Errorf("IsRestrictedIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestSetAllowedCIDRs_Narrow(t *testing.T) {
	t.Cleanup(func() { _, _ = SetAllowedCIDRs("") })
	if _, err := SetAllowedCIDRs("10.28.0.0/16"); err != nil {
		t.Fatalf("SetAllowedCIDRs: %v", err)
	}
	if IsRestrictedIP(net.ParseIP("10.28.92.7")) {
		t.Error("10.28.92.7 should be allowed")
	}
	if !IsRestrictedIP(net.ParseIP("10.99.1.1")) {
		t.Error("10.99.1.1 is outside the allowed CIDR and should stay blocked")
	}
}

func TestSetAllowedCIDRs_Empty(t *testing.T) {
	applied, err := SetAllowedCIDRs("")
	if err != nil || len(applied) != 0 {
		t.Fatalf("SetAllowedCIDRs(\"\") = %v, %v; want no entries, no error", applied, err)
	}
	if !IsRestrictedIP(net.ParseIP("10.28.92.7")) {
		t.Error("private IP must stay blocked when no CIDRs are allowed")
	}
}

func TestSetAllowedCIDRs_Invalid(t *testing.T) {
	t.Cleanup(func() { _, _ = SetAllowedCIDRs("") })
	if _, err := SetAllowedCIDRs("10.28.0.0/16,not-a-cidr"); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestSetAllowedCIDRs_IPv6CannotReopenNAT64(t *testing.T) {
	t.Cleanup(func() { _, _ = SetAllowedCIDRs("") })
	if _, err := SetAllowedCIDRs("::/0,0.0.0.0/0"); err != nil {
		t.Fatalf("SetAllowedCIDRs: %v", err)
	}
	for _, ip := range []string{"64:ff9b::a9fe:a9fe", "64:ff9b::7f00:1", "64:ff9b:1::1", "fe80::1"} {
		if !IsRestrictedIP(net.ParseIP(ip)) {
			t.Errorf("IsRestrictedIP(%s) = false, want true even with ::/0 allowed", ip)
		}
	}
	if IsRestrictedIP(net.ParseIP("fd00::1")) {
		t.Error("fd00::1 (ULA) should be allowed by ::/0")
	}
}
