package server

import "testing"

// allowVerify unit coverage: the fixed window admits verifyRateMax attempts
// per address and refuses the next one, without interfering with other
// addresses.
func TestAllowVerifyRateLimit(t *testing.T) {
	var s Server
	for i := 0; i < verifyRateMax; i++ {
		if !s.allowVerify("10.0.0.1") {
			t.Fatalf("attempt %d refused, want allowed (cap %d)", i+1, verifyRateMax)
		}
	}
	if s.allowVerify("10.0.0.1") {
		t.Fatal("attempt beyond the cap allowed")
	}
	// Another address has its own window.
	if !s.allowVerify("10.0.0.2") {
		t.Error("second address refused inside the first address's cap")
	}
}
