package permissions

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/juancer/milo/internal/store"
)

func setup(t *testing.T) (*Checker, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewChecker(st), st
}

func user(t *testing.T, st *store.Store, role Role) int64 {
	t.Helper()
	id, err := st.CreateUser(context.Background(), "test", string(role))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRoleDefaults(t *testing.T) {
	ctx := context.Background()
	c, st := setup(t)
	cases := []struct {
		role Role
		perm Permission
		want bool
	}{
		{RoleOwner, ManageCredentials, true},
		{RoleOwner, EmergencyAlert, true},
		{RoleParent, EmergencyAlert, true},
		{RoleParent, ManageConfiguration, false},
		{RoleParent, ManageCredentials, false},
		{RoleFamily, SendMessage, true},
		{RoleFamily, EmergencyAlert, false},
		{RoleChild, SendMessage, false},
		{RoleChild, EmergencyAlert, false},
	}
	for _, tc := range cases {
		got, err := c.Has(ctx, user(t, st, tc.role), tc.perm)
		if err != nil || got != tc.want {
			t.Errorf("%s/%s = %v, %v; want %v", tc.role, tc.perm, got, err, tc.want)
		}
	}
}

func TestExplicitOverridesBeatRoleDefaults(t *testing.T) {
	ctx := context.Background()
	c, st := setup(t)
	grandma := user(t, st, RoleFamily)
	if err := c.Set(ctx, grandma, EmergencyAlert, true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Has(ctx, grandma, EmergencyAlert); !ok {
		t.Fatal("explicit grant ignored")
	}
	dad := user(t, st, RoleParent)
	c.Set(ctx, dad, EmergencyAlert, false)
	if ok, _ := c.Has(ctx, dad, EmergencyAlert); ok {
		t.Fatal("explicit denial ignored")
	}
}

func TestUnknownUserAndPermissionAreDenied(t *testing.T) {
	ctx := context.Background()
	c, st := setup(t)
	if ok, err := c.Has(ctx, 12345, SendMessage); ok || err != nil {
		t.Fatalf("unknown user = %v, %v", ok, err)
	}
	if ok, _ := c.Has(ctx, user(t, st, RoleOwner), Permission("shell_access")); ok {
		t.Fatal("unknown permission must be denied even for OWNER")
	}
	if err := c.Set(ctx, 1, Permission("shell_access"), true); err == nil {
		t.Fatal("Set must reject unknown permissions")
	}
}

func TestValidators(t *testing.T) {
	if !ValidRole(RoleChild) || ValidRole("ROOT") {
		t.Fatal("ValidRole wrong")
	}
	if len(Defaults(RoleOwner)) != len(all) || len(Defaults(RoleChild)) != 0 {
		t.Fatal("Defaults wrong")
	}
}

func TestDeactivatedUserHasNoPermissions(t *testing.T) {
	ctx := context.Background()
	c, st := setup(t)
	id := user(t, st, RoleOwner)
	if err := st.UpdateUser(ctx, id, "x", "OWNER", false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Has(ctx, id, SendMessage); ok {
		t.Fatal("deactivated OWNER still has permissions")
	}
}
