package config

import "testing"

func roleEnv(extra map[string]string) func(string) string {
	env := map[string]string{"HM_DATABASE_URL": "postgres://example"}
	for k, v := range extra {
		env[k] = v
	}
	return envFromMap(env)
}

func TestConfig_RoleDefaultsToAll(t *testing.T) {
	cfg, err := Load(roleEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Role != RoleAll {
		t.Errorf("Role = %q, want all", cfg.Role)
	}
	for _, r := range []Role{RoleAll, RoleWeb, RolePoller, RoleCoordinator} {
		cfg, err := Load(roleEnv(map[string]string{"HM_ROLE": string(r)}))
		if err != nil || cfg.Role != r {
			t.Errorf("HM_ROLE=%s: role %q, err %v", r, cfg.Role, err)
		}
	}
}

func TestConfig_RoleRejectsUnknown(t *testing.T) {
	_, err := Load(roleEnv(map[string]string{"HM_ROLE": "worker"}))
	if err == nil || err.Error() != "HM_ROLE: must be all, web, poller or coordinator" {
		t.Fatalf("err = %v", err)
	}
	if _, err := ParseRole("Web"); err == nil {
		t.Error("ParseRole accepted a different case")
	}
}

func TestRole_Capabilities(t *testing.T) {
	for _, tc := range []struct {
		role                    Role
		api, polls, coordinates bool
	}{
		{RoleAll, true, true, true},
		{RoleWeb, true, false, false},
		{RolePoller, false, true, false},
		{RoleCoordinator, false, false, true},
	} {
		if tc.role.ServesAPI() != tc.api || tc.role.Polls() != tc.polls || tc.role.Coordinates() != tc.coordinates {
			t.Errorf("%s: api %v polls %v coordinates %v", tc.role, tc.role.ServesAPI(), tc.role.Polls(), tc.role.Coordinates())
		}
	}
}

func TestConfig_PoolSizePerRole(t *testing.T) {
	for role, want := range map[Role]int{RoleAll: 0, RoleWeb: 6, RolePoller: 8, RoleCoordinator: 4} {
		if got := (Config{Role: role}).PoolSize(); got != want {
			t.Errorf("%s: PoolSize = %d, want %d", role, got, want)
		}
	}
	if got := (Config{Role: RolePoller, DBMaxConns: 20}).PoolSize(); got != 20 {
		t.Errorf("explicit value: PoolSize = %d, want 20", got)
	}
}

func TestConfig_DBMaxConnsRejectsOneAndTwo(t *testing.T) {
	for _, v := range []string{"1", "2", "-1"} {
		if _, err := Load(roleEnv(map[string]string{"HM_DB_MAX_CONNS": v})); err == nil {
			t.Errorf("HM_DB_MAX_CONNS=%s accepted", v)
		}
	}
	for _, v := range []string{"0", "3", "40"} {
		if _, err := Load(roleEnv(map[string]string{"HM_DB_MAX_CONNS": v})); err != nil {
			t.Errorf("HM_DB_MAX_CONNS=%s: %v", v, err)
		}
	}
}
