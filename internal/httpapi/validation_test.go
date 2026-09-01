package httpapi

import "testing"

// White-box: parseSingleAddress is unexported, same reason health_test.go and
// stubdriver_test.go already live in package httpapi rather than httpapi_test.
func TestParseSingleAddressNormalizes(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"cust@example.com", "cust@example.com"},
		{"Display Name <cust@example.com>", "cust@example.com"},
	} {
		got, err := parseSingleAddress(tc.in)
		if err != nil {
			t.Errorf("parseSingleAddress(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSingleAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"", "not-an-email", "one@example.com, two@example.com"} {
		if _, err := parseSingleAddress(bad); err == nil {
			t.Errorf("parseSingleAddress(%q) accepted an invalid address", bad)
		}
	}
}
