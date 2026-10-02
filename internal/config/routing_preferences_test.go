package config

import "testing"

func TestPreferredAccountsConfigValidation(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("routing:\n  preferred-accounts:\n    codex: '  example-index  '\n    claude: ''\n"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if cfg.Routing.PreferredAccounts["codex"] != "example-index" || len(cfg.Routing.PreferredAccounts) != 1 {
		t.Fatalf("normalized preferences = %+v", cfg.Routing.PreferredAccounts)
	}
	if _, errInvalid := ParseConfigBytes([]byte("routing:\n  preferred-accounts:\n    unknown: example-index\n")); errInvalid == nil {
		t.Fatal("accepted unsupported preferred provider")
	}
	clone := cfg.CloneForRuntime()
	cfg.Routing.PreferredAccounts["codex"] = "changed-index"
	if clone.Routing.PreferredAccounts["codex"] != "example-index" {
		t.Fatal("runtime preferences share mutable configuration map")
	}
}
