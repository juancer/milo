// Package permissions decides what a user may do. It is shared by the web
// admin, Telegram and the Action Engine.
//
// A user's effective permissions are their role defaults, overridden by
// explicit per-user grants or denials stored in SQLite.
package permissions

import (
	"context"
	"errors"
	"fmt"

	"github.com/juancer/nexo/internal/store"
)

// Role is a named bundle of default permissions.
type Role string

const (
	RoleOwner  Role = "OWNER"
	RoleParent Role = "PARENT"
	RoleFamily Role = "FAMILY"
	RoleChild  Role = "CHILD"
)

// Permission is a single capability.
type Permission string

const (
	SendMessage         Permission = "send_message"
	ReceiveMessage      Permission = "receive_message"
	EmergencyAlert      Permission = "emergency_alert"
	ManageContacts      Permission = "manage_contacts"
	ManageConfiguration Permission = "manage_configuration"
	ManageFeatures      Permission = "manage_features"
	ManageSchedules     Permission = "manage_schedules"
	ManageCredentials   Permission = "manage_credentials"
	ManageUsers         Permission = "manage_users"
	ManageInvitations   Permission = "manage_invitations"
)

var all = []Permission{
	SendMessage, ReceiveMessage, EmergencyAlert, ManageContacts, ManageConfiguration,
	ManageFeatures, ManageSchedules, ManageCredentials, ManageUsers, ManageInvitations,
}

var roleDefaults = map[Role][]Permission{
	RoleOwner:  all,
	RoleParent: {SendMessage, ReceiveMessage, EmergencyAlert, ManageContacts, ManageSchedules},
	RoleFamily: {SendMessage, ReceiveMessage},
	// Child device interactions go through the Action Engine (a later phase),
	// so a child holds no Telegram or management permissions by default.
	RoleChild: nil,
}

// ValidPermission reports whether p is a known permission.
func ValidPermission(p Permission) bool {
	for _, k := range all {
		if k == p {
			return true
		}
	}
	return false
}

// ValidRole reports whether r is a known role.
func ValidRole(r Role) bool {
	_, ok := roleDefaults[r]
	return ok
}

// Defaults returns the default permissions of a role.
func Defaults(r Role) []Permission { return append([]Permission(nil), roleDefaults[r]...) }

// Checker evaluates permissions against the database.
type Checker struct {
	store *store.Store
}

// NewChecker returns a Checker backed by s.
func NewChecker(s *store.Store) *Checker { return &Checker{store: s} }

// Has reports whether the user holds permission p. Unknown users and unknown
// permissions are always denied.
func (c *Checker) Has(ctx context.Context, userID int64, p Permission) (bool, error) {
	if !ValidPermission(p) {
		return false, nil
	}
	u, err := c.store.GetUser(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	overrides, err := c.store.UserPermissions(ctx, userID)
	if err != nil {
		return false, err
	}
	if granted, ok := overrides[string(p)]; ok {
		return granted, nil
	}
	for _, d := range roleDefaults[Role(u.Role)] {
		if d == p {
			return true, nil
		}
	}
	return false, nil
}

// Set records an explicit grant or denial for a user.
func (c *Checker) Set(ctx context.Context, userID int64, p Permission, granted bool) error {
	if !ValidPermission(p) {
		return fmt.Errorf("permissions: unknown permission %q", p)
	}
	return c.store.SetPermission(ctx, userID, string(p), granted)
}
