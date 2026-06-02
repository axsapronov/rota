package services

import "testing"

func TestBuildGeoBatch_PreFailedInternalRanges(t *testing.T) {
	addresses := []string{
		"8.8.8.8:8080",
		"0.103.177.131:3128",
		"100.110.6.97:1080",
		"not-an-ip:9999",
		"8.8.8.8:8081",
	}

	ipToAddrs, ips, preFailed := buildGeoBatch(addresses)
	if len(ips) != 1 || ips[0] != "8.8.8.8" {
		t.Fatalf("ips = %v, want [8.8.8.8]", ips)
	}
	if len(ipToAddrs["8.8.8.8"]) != 2 {
		t.Fatalf("ipToAddrs[8.8.8.8] = %v, want 2 addresses", ipToAddrs["8.8.8.8"])
	}
	if len(preFailed) != 3 {
		t.Fatalf("preFailed len = %d, want 3", len(preFailed))
	}

	var hasZeroRange bool
	var hasCGNAT bool
	var hasInvalid bool
	for _, f := range preFailed {
		switch f.Address {
		case "0.103.177.131:3128":
			hasZeroRange = f.Reason == geoSkipReasonInternalReserved
		case "100.110.6.97:1080":
			hasCGNAT = f.Reason == geoSkipReasonInternalReserved
		case "not-an-ip:9999":
			hasInvalid = f.Reason == geoSkipReasonInvalidAddress
		}
	}
	if !hasZeroRange || !hasCGNAT || !hasInvalid {
		t.Fatalf("unexpected preFailed reasons: %+v", preFailed)
	}
}
