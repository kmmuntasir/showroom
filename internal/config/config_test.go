package config

import (
	"strings"
	"testing"
)

func testEnv() map[string]string {
	return map[string]string{
		"DEMOCTL_CONTROL_HOST":    "demos.example.com",
		"DEMOCTL_BASE_DOMAIN":     "example.com",
		"DEMOCTL_SESSION_KEY":     strings.Repeat("k", 32),
		"GOOGLE_CLIENT_ID":        "client-id",
		"GOOGLE_CLIENT_SECRET":    "client-secret",
		"GOOGLE_WORKSPACE_DOMAIN": "example.com",
	}
}

func loader(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestLoadHappy(t *testing.T) {
	cfg, err := Load(loader(testEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != DefaultListen {
		t.Errorf("Listen = %q, want default %q", cfg.Listen, DefaultListen)
	}
	if cfg.ControlHost != "demos.example.com" || cfg.BaseDomain != "example.com" {
		t.Errorf("hosts = %q / %q", cfg.ControlHost, cfg.BaseDomain)
	}
	if cfg.WorkspaceDomain != "example.com" {
		t.Errorf("WorkspaceDomain = %q", cfg.WorkspaceDomain)
	}
	if cfg.Limits.MaxZipBytes != 100<<20 {
		t.Errorf("MaxZipBytes = %d", cfg.Limits.MaxZipBytes)
	}
}

func TestLoadDefaults(t *testing.T) {
	env := testEnv()
	cfg, err := Load(loader(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for name, got := range map[string]string{
		"DataDir":   cfg.DataDir,
		"DBPath":    cfg.DBPath,
		"AuditPath": cfg.AuditPath,
	} {
		if got == "" {
			t.Errorf("%s: default not applied", name)
		}
	}
}

func TestLoadMissingRequired(t *testing.T) {
	for _, name := range []string{
		"DEMOCTL_CONTROL_HOST", "DEMOCTL_BASE_DOMAIN", "DEMOCTL_SESSION_KEY",
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_WORKSPACE_DOMAIN",
	} {
		env := testEnv()
		delete(env, name)
		if _, err := Load(loader(env)); err == nil {
			t.Errorf("deleting %s: want error, got nil", name)
		}
	}
}

func TestLoadBadValues(t *testing.T) {
	cases := map[string]func(map[string]string){
		"listen not host:port":   func(e map[string]string) { e["DEMOCTL_LISTEN"] = "nope" },
		"control host with URL":  func(e map[string]string) { e["DEMOCTL_CONTROL_HOST"] = "https://demos.example.com" },
		"control host uppercase": func(e map[string]string) { e["DEMOCTL_CONTROL_HOST"] = "Demos.Example.com" },
		"control equals base":    func(e map[string]string) { e["DEMOCTL_CONTROL_HOST"] = "example.com" },
		"short session key":      func(e map[string]string) { e["DEMOCTL_SESSION_KEY"] = "short" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := testEnv()
			mutate(env)
			if _, err := Load(loader(env)); err == nil {
				t.Fatalf("want error, got nil")
			}
		})
	}
}

func passwordEnv() map[string]string {
	env := testEnv()
	env["DEMOCTL_AUTH_MODE"] = "password"
	env["DEMOCTL_ADMIN_EMAIL"] = "admin@example.com"
	env["DEMOCTL_ADMIN_PASSWORD"] = strings.Repeat("p", MinPasswordLength)
	delete(env, "GOOGLE_CLIENT_ID")
	delete(env, "GOOGLE_CLIENT_SECRET")
	delete(env, "GOOGLE_WORKSPACE_DOMAIN")
	return env
}

func TestLoadAuthModeDefaultsToGoogle(t *testing.T) {
	cfg, err := Load(loader(testEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuthMode != AuthModeGoogle {
		t.Errorf("AuthMode = %q, want %q", cfg.AuthMode, AuthModeGoogle)
	}
}

func TestLoadBadAuthMode(t *testing.T) {
	env := testEnv()
	env["DEMOCTL_AUTH_MODE"] = "ldap"
	if _, err := Load(loader(env)); err == nil {
		t.Error("want error for unknown auth mode, got nil")
	}
}

func TestLoadPasswordModeHappy(t *testing.T) {
	cfg, err := Load(loader(passwordEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuthMode != AuthModePassword {
		t.Errorf("AuthMode = %q, want %q", cfg.AuthMode, AuthModePassword)
	}
	if cfg.AdminEmail != "admin@example.com" {
		t.Errorf("AdminEmail = %q", cfg.AdminEmail)
	}
	if cfg.GoogleClientID != "" || cfg.GoogleClientSecret != "" || cfg.WorkspaceDomain != "" {
		t.Error("google fields must stay empty in password mode")
	}
}

func TestLoadPasswordModeNormalizes(t *testing.T) {
	env := passwordEnv()
	env["DEMOCTL_AUTH_MODE"] = " Password "
	env["DEMOCTL_ADMIN_EMAIL"] = " Admin@Example.COM "
	cfg, err := Load(loader(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuthMode != AuthModePassword {
		t.Errorf("AuthMode = %q, want normalized %q", cfg.AuthMode, AuthModePassword)
	}
	if cfg.AdminEmail != "admin@example.com" {
		t.Errorf("AdminEmail = %q, want lowercased", cfg.AdminEmail)
	}
}

func TestLoadPasswordModeMissingAdmin(t *testing.T) {
	for _, name := range []string{"DEMOCTL_ADMIN_EMAIL", "DEMOCTL_ADMIN_PASSWORD"} {
		env := passwordEnv()
		delete(env, name)
		if _, err := Load(loader(env)); err == nil {
			t.Errorf("deleting %s: want error, got nil", name)
		}
	}
}

func TestLoadPasswordModeBadAdmin(t *testing.T) {
	cases := map[string]func(map[string]string){
		"email without @":     func(e map[string]string) { e["DEMOCTL_ADMIN_EMAIL"] = "not-an-email" },
		"email with space":    func(e map[string]string) { e["DEMOCTL_ADMIN_EMAIL"] = "a @example.com" },
		"short password":      func(e map[string]string) { e["DEMOCTL_ADMIN_PASSWORD"] = "short" },
		"one char short":      func(e map[string]string) { e["DEMOCTL_ADMIN_PASSWORD"] = strings.Repeat("p", MinPasswordLength-1) },
		"google vars missing": func(e map[string]string) {}, // must still load: google is not required here
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := passwordEnv()
			mutate(env)
			_, err := Load(loader(env))
			if name == "google vars missing" {
				if err != nil {
					t.Fatalf("want nil (google not required in password mode), got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestLoadGoogleModeStillRequiresGoogle(t *testing.T) {
	env := testEnv()
	env["DEMOCTL_AUTH_MODE"] = "google"
	delete(env, "GOOGLE_CLIENT_ID")
	if _, err := Load(loader(env)); err == nil {
		t.Error("deleting GOOGLE_CLIENT_ID in google mode: want error, got nil")
	}
}
