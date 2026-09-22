package demonames

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"simple", "acme-demo", true},
		{"digits", "demo2026", true},
		{"min length", "ab", true},
		{"max length", strings.Repeat("a", 32), true},
		{"too short", "a", false},
		{"too long", strings.Repeat("a", 33), false},
		{"leading hyphen", "-demo", false},
		{"trailing hyphen", "demo-", false},
		{"uppercase", "Acme", false},
		{"underscore", "acme_demo", false},
		{"dot", "acme.demo", false},
		{"empty", "", false},
		{"space", "acme demo", false},
		{"single hyphen", "-", false},
	}
	for _, tc := range cases {
		if got := Valid(tc.in); got != tc.want {
			t.Errorf("Valid(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCheckReserved(t *testing.T) {
	for _, name := range []string{"gateway", "gitlab", "mdm", "www", "api", "demos"} {
		if err := Check(name); !errors.Is(err, ErrReserved) {
			t.Errorf("Check(%q) = %v, want ErrReserved", name, err)
		}
	}
}

func TestCheckValid(t *testing.T) {
	if err := Check("acme-demo"); err != nil {
		t.Errorf("Check(acme-demo) = %v, want nil", err)
	}
}

func TestCheckInvalid(t *testing.T) {
	for _, name := range []string{"a", "-bad", "Bad", "bad_name", ""} {
		if err := Check(name); !errors.Is(err, ErrInvalid) {
			t.Errorf("Check(%q) = %v, want ErrInvalid", name, err)
		}
	}
}

func TestReservedNamesSortedAndComplete(t *testing.T) {
	names := ReservedNames()
	if len(names) == 0 {
		t.Fatal("ReservedNames is empty")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("ReservedNames not sorted at %q", names[i])
		}
	}
	// Deployed hosts on the base domain must all be present.
	for _, want := range []string{"gateway", "pma", "pga", "proxmox", "mdm", "gitlab", "jira", "vault", "wiki", "test", "admin"} {
		if !slices.Contains(names, want) {
			t.Errorf("reserved list missing deployed host %q", want)
		}
	}
}
