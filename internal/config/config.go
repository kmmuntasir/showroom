// Package config loads democtl configuration from the environment
// (docs/demos.md §democtl service). Env-only like the panel: the systemd
// unit points at /etc/democtl/democtl.env, and docs carry variable names
// only — never values.
package config

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

const (
	DefaultListen    = "127.0.0.1:5000"
	DefaultDataDir   = "/srv/democtl"
	DefaultDBPath    = "/var/lib/democtl/democtl.db"
	DefaultAuditPath = "/var/lib/democtl/audit.jsonl"
	// SessionKeyBytes is the minimum length of DEMOCTL_SESSION_KEY — 32
	// bytes of entropy minimum so cookie signing never leans on a short
	// secret (docs/demos.md §Auth).
	SessionKeyBytes = 32
	// AuthModeGoogle is the default: Google Workspace OAuth. AuthModePassword
	// switches the control plane to local email/password logins backed by
	// the users table; the mode is deployment-wide and picked via
	// DEMOCTL_AUTH_MODE.
	AuthModeGoogle   = "google"
	AuthModePassword = "password"
	// MinPasswordLength is the minimum bootstrap superadmin password length
	// in password mode. It applies to the env-provided credential only;
	// per-user password policy lives in the auth package.
	MinPasswordLength = 12
)

// Limits are deployment policy, not operator configuration — they ship as
// constants (docs/demos.md §Resource budget) and tests inject their own.
type Limits struct {
	MaxZipBytes   int64 // compressed upload cap
	MaxTotalBytes int64 // uncompressed total across the archive
	MaxFileBytes  int64 // per-entry cap
	MaxFiles      int   // entry-count cap
	ReleaseKeep   int   // releases kept per demo (current + one rollback)
}

// DefaultLimits — docs/demos.md §Upload pipeline limits table.
func DefaultLimits() Limits {
	return Limits{
		MaxZipBytes:   100 << 20, // 100 MB
		MaxTotalBytes: 300 << 20, // 300 MB
		MaxFileBytes:  50 << 20,  // 50 MB
		MaxFiles:      2000,
		ReleaseKeep:   2, // current + previous for one-click rollback
	}
}

type Config struct {
	Listen      string // LAN-only bind address, e.g. 192.0.2.10:5000
	ControlHost string // dashboard hostname, e.g. demos.example.com
	BaseDomain  string // demo hosts are <name>.<BaseDomain>, e.g. example.com
	DataDir     string // demos/, releases/, tmp/ live here
	DBPath      string
	AuditPath   string
	SessionKey  string

	// AuthMode is AuthModeGoogle (default) or AuthModePassword.
	AuthMode string

	GoogleClientID     string
	GoogleClientSecret string
	WorkspaceDomain    string // Google Workspace domain — enforced server-side at callback

	// AdminEmail/AdminPassword bootstrap the superadmin in password mode;
	// the account is created at boot only if it does not exist yet.
	AdminEmail    string
	AdminPassword string

	Limits Limits
}

var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// Load reads every variable from getenv (injectable for tests) and
// validates the whole set — a misconfigured service must refuse to boot.
func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	var errs []error

	req := func(dst *string, name string) {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
			return
		}
		*dst = v
	}

	cfg.Listen = getenv("DEMOCTL_LISTEN")
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		errs = append(errs, fmt.Errorf("DEMOCTL_LISTEN %q is not host:port", cfg.Listen))
	}

	req(&cfg.ControlHost, "DEMOCTL_CONTROL_HOST")
	req(&cfg.BaseDomain, "DEMOCTL_BASE_DOMAIN")
	for name, v := range map[string]string{
		"DEMOCTL_CONTROL_HOST": cfg.ControlHost,
		"DEMOCTL_BASE_DOMAIN":  cfg.BaseDomain,
	} {
		if v == "" || strings.ContainsAny(v, "/: ") || !hostnameRe.MatchString(v) {
			errs = append(errs, fmt.Errorf("%s %q is not a bare lowercase hostname", name, v))
		}
	}
	if cfg.ControlHost == cfg.BaseDomain {
		// The control host is itself a subdomain of the base; if it ever
		// equals the base, the host mux becomes ambiguous.
		errs = append(errs, errors.New("DEMOCTL_CONTROL_HOST must differ from DEMOCTL_BASE_DOMAIN"))
	}

	cfg.DataDir = getenv("DEMOCTL_DATA_DIR")
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	cfg.DBPath = getenv("DEMOCTL_DB_PATH")
	if cfg.DBPath == "" {
		cfg.DBPath = DefaultDBPath
	}
	cfg.AuditPath = getenv("DEMOCTL_AUDIT_PATH")
	if cfg.AuditPath == "" {
		cfg.AuditPath = DefaultAuditPath
	}

	req(&cfg.SessionKey, "DEMOCTL_SESSION_KEY")
	if cfg.SessionKey != "" && len(cfg.SessionKey) < SessionKeyBytes {
		errs = append(errs, fmt.Errorf("DEMOCTL_SESSION_KEY must be at least %d bytes", SessionKeyBytes))
	}

	// The auth mode is deployment-wide: exactly one login mechanism is
	// active. Empty keeps the historical default (Google OAuth) so existing
	// deployments boot unchanged.
	cfg.AuthMode = strings.ToLower(strings.TrimSpace(getenv("DEMOCTL_AUTH_MODE")))
	if cfg.AuthMode == "" {
		cfg.AuthMode = AuthModeGoogle
	}
	if cfg.AuthMode != AuthModeGoogle && cfg.AuthMode != AuthModePassword {
		errs = append(errs, fmt.Errorf("DEMOCTL_AUTH_MODE %q must be %q or %q", cfg.AuthMode, AuthModeGoogle, AuthModePassword))
	}

	if cfg.AuthMode == AuthModeGoogle {
		req(&cfg.GoogleClientID, "GOOGLE_CLIENT_ID")
		req(&cfg.GoogleClientSecret, "GOOGLE_CLIENT_SECRET")
		req(&cfg.WorkspaceDomain, "GOOGLE_WORKSPACE_DOMAIN")
		cfg.WorkspaceDomain = strings.ToLower(cfg.WorkspaceDomain)
	} else if cfg.AuthMode == AuthModePassword {
		// Google credentials are meaningless here and must not be
		// required; the bootstrap superadmin takes their place.
		email := strings.ToLower(strings.TrimSpace(getenv("DEMOCTL_ADMIN_EMAIL")))
		if email == "" || !strings.Contains(email, "@") || strings.Contains(email, " ") {
			errs = append(errs, errors.New("DEMOCTL_ADMIN_EMAIL is required and must be a valid email address"))
		} else {
			cfg.AdminEmail = email
		}
		// The password is used verbatim — never trimmed (spaces can be
		// significant) and never echoed back in an error.
		password := getenv("DEMOCTL_ADMIN_PASSWORD")
		if len(password) < MinPasswordLength {
			errs = append(errs, fmt.Errorf("DEMOCTL_ADMIN_PASSWORD must be at least %d characters", MinPasswordLength))
		} else {
			cfg.AdminPassword = password
		}
	}

	cfg.Limits = DefaultLimits()

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// LoadOS is Load over the process environment.
func LoadOS() (Config, error) {
	return Load(nil) // replaced by main with os.Getenv; kept for symmetry in tests
}
