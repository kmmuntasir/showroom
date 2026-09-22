// Package demonames validates demo subdomain labels and enforces the
// reserved list. The wildcard proxy route lands every unmatched
// *.example.com host on democtl, so the service itself must refuse names
// that collide with existing or future hosts on the base domain.
package demonames

import (
	"errors"
	"slices"
	"strings"
)

// ErrInvalid and ErrReserved are the two failure modes of Check.
var (
	ErrInvalid  = errors.New("demonames: invalid name")
	ErrReserved = errors.New("demonames: name is reserved")
)

// reserved carries every hostname deployed on the base domain plus the
// generic infra blocklist. Any new host must be added here in the same
// commit that adds its proxy rule.
var reserved = map[string]struct{}{
	// Deployed hosts on the base domain.
	"gateway": {}, "pma": {}, "pga": {}, "proxmox": {}, "mdm": {},
	"gitlab": {}, "jira": {}, "vault": {}, "wiki": {},
	"test": {}, "admin": {},
	// Infra-generic blocklist.
	"www": {}, "api": {}, "app": {}, "mail": {}, "smtp": {}, "imap": {},
	"pop": {}, "mx": {}, "webmail": {}, "autodiscover": {}, "ns1": {},
	"ns2": {}, "vpn": {}, "wg": {}, "git": {}, "ci": {}, "cd": {},
	"status": {}, "docs": {}, "blog": {}, "shop": {}, "dev": {},
	"staging": {}, "panel": {}, "db": {}, "devdb": {}, "demos": {},
	"demo": {}, "monitor": {}, "grafana": {}, "relay": {}, "sso": {},
	"auth": {}, "login": {}, "cdn": {}, "static": {}, "assets": {},
	"support": {}, "help": {}, "billing": {}, "pay": {}, "secure": {},
	"account": {}, "accounts": {},
}

// Valid reports whether name is a well-formed demo label: 2–32 chars of
// lowercase ASCII letters/digits/hyphens, starting and ending with a
// letter or digit, no leading/trailing hyphen.
func Valid(name string) bool {
	if len(name) < 2 || len(name) > 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		case b == '-':
			if i == 0 || i == len(name)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// IsReserved reports whether name collides with an estate host or the
// infra blocklist.
func IsReserved(name string) bool {
	_, ok := reserved[strings.ToLower(name)]
	return ok
}

// Check is the handler-edge gate: nil when a demo may claim name,
// ErrReserved or ErrInvalid otherwise.
func Check(name string) error {
	if IsReserved(name) {
		return ErrReserved
	}
	if !Valid(name) {
		return ErrInvalid
	}
	return nil
}

// ReservedNames returns the sorted reserved list, for tests and docs.
func ReservedNames() []string {
	out := make([]string, 0, len(reserved))
	for name := range reserved {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
