package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/store"
)

// Errors returned by Accounts.
var (
	ErrBadCredentials = errors.New("incorrect username or password")
	ErrUsername       = errors.New("usernames are 2-24 characters: lowercase letters, digits, - and _, starting with a letter")
	ErrUsernameTaken  = errors.New("that username is already used")
	ErrLastOwner      = errors.New("the owner account cannot be removed")
	ErrSelf           = errors.New("you cannot remove your own account")
)

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,23}$`)

// ValidateUsername checks an admin username.
func ValidateUsername(u string) error {
	if !usernameRe.MatchString(u) {
		return ErrUsername
	}
	return nil
}

// alphabet has no look-alike characters (0/O, 1/l/I) so a printed password can be typed reliably.
const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// GeneratePassword returns a random password of 20 characters (about 100 bits)
// in five groups of four: "k3vq-h8ne-w2xd-m9tr-c4jb".
func GeneratePassword() (string, error) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}

// Accounts manages admin users.
type Accounts struct {
	store     *store.Store
	dummyHash string // compared against for unknown usernames, so timing gives nothing away
	now       func() time.Time
}

// NewAccounts returns an Accounts service.
func NewAccounts(s *store.Store) *Accounts {
	h, _ := HashPassword("not-a-real-password-just-timing")
	return &Accounts{store: s, dummyHash: h, now: time.Now}
}

// FirstAdminUsername is the name of the account created at first start.
const FirstAdminUsername = "admin"

// EnsureFirstAdmin creates the owner account with a generated password if no
// admin exists yet. The password is returned once so the caller can show it to
// the person installing the device; it is never stored or logged. The account
// must change it at first sign-in.
func (a *Accounts) EnsureFirstAdmin(ctx context.Context) (created bool, username, password string, err error) {
	admins, err := a.store.ListAdmins(ctx)
	if err != nil || len(admins) > 0 {
		return false, "", "", err
	}
	password, err = GeneratePassword()
	if err != nil {
		return false, "", "", err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, "", "", err
	}
	if _, err := a.store.CreateAdmin(ctx, FirstAdminUsername, hash, true, true); err != nil {
		return false, "", "", err
	}
	return true, FirstAdminUsername, password, nil
}

// Authenticate checks a username and password.
func (a *Accounts) Authenticate(ctx context.Context, username, password string) (store.AdminUser, error) {
	u, err := a.store.GetAdminByName(ctx, strings.ToLower(strings.TrimSpace(username)))
	if errors.Is(err, store.ErrNotFound) {
		if _, err := VerifyPasswordContext(ctx, password, a.dummyHash); errors.Is(err, ErrBusy) {
			return store.AdminUser{}, err
		}
		return store.AdminUser{}, ErrBadCredentials
	}
	if err != nil {
		return store.AdminUser{}, err
	}
	ok, err := VerifyPasswordContext(ctx, password, u.PasswordHash)
	if err != nil {
		return store.AdminUser{}, err
	}
	if !ok {
		return store.AdminUser{}, ErrBadCredentials
	}
	a.store.TouchAdminLogin(ctx, u.ID, a.now()) //nolint:errcheck
	return u, nil
}

// Create adds an admin. With an empty password one is generated and returned
// (shown to the creator once); either way the new admin must change it at the
// first sign-in.
func (a *Accounts) Create(ctx context.Context, username, password string) (user store.AdminUser, generated string, err error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if err := ValidateUsername(username); err != nil {
		return store.AdminUser{}, "", err
	}
	if password == "" {
		if password, err = GeneratePassword(); err != nil {
			return store.AdminUser{}, "", err
		}
		generated = password
	} else if err := ValidatePassword(password); err != nil {
		return store.AdminUser{}, "", err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return store.AdminUser{}, "", err
	}
	id, err := a.store.CreateAdmin(ctx, username, hash, false, true)
	if err != nil {
		if _, e := a.store.GetAdminByName(ctx, username); e == nil {
			return store.AdminUser{}, "", ErrUsernameTaken
		}
		return store.AdminUser{}, "", err
	}
	user, err = a.store.GetAdmin(ctx, id)
	return user, generated, err
}

// ChangePassword lets a user replace their own password after proving the current one.
func (a *Accounts) ChangePassword(ctx context.Context, id int64, current, next string) error {
	u, err := a.store.GetAdmin(ctx, id)
	if err != nil {
		return err
	}
	ok, err := VerifyPasswordContext(ctx, current, u.PasswordHash)
	if errors.Is(err, ErrBusy) {
		return err
	}
	if err != nil || !ok {
		return ErrBadCredentials
	}
	if err := ValidatePassword(next); err != nil {
		return err
	}
	if next == current {
		return errors.New("choose a different password")
	}
	hash, err := HashPasswordContext(ctx, next)
	if err != nil {
		return err
	}
	return a.store.SetAdminPassword(ctx, id, hash, false)
}

// SetPassword sets a password directly (used from the command line by someone
// with shell access). mustChange forces the person to choose their own next.
func (a *Accounts) SetPassword(ctx context.Context, id int64, password string, mustChange bool) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return a.store.SetAdminPassword(ctx, id, hash, mustChange)
}

// Reset gives an admin a new generated password, returned once. They must change it at next sign-in.
func (a *Accounts) Reset(ctx context.Context, id int64) (string, error) {
	pw, err := GeneratePassword()
	if err != nil {
		return "", err
	}
	if err := a.SetPassword(ctx, id, pw, true); err != nil {
		return "", err
	}
	return pw, nil
}

// Delete removes an admin. The owner and the account doing the deleting are protected.
func (a *Accounts) Delete(ctx context.Context, actorID, id int64) error {
	if id == actorID {
		return ErrSelf
	}
	u, err := a.store.GetAdmin(ctx, id)
	if err != nil {
		return err
	}
	if u.IsOwner {
		return ErrLastOwner
	}
	return a.store.DeleteAdmin(ctx, id)
}

// Owner returns the owner account.
func (a *Accounts) Owner(ctx context.Context) (store.AdminUser, error) {
	admins, err := a.store.ListAdmins(ctx)
	if err != nil {
		return store.AdminUser{}, err
	}
	for _, u := range admins {
		if u.IsOwner {
			return u, nil
		}
	}
	return store.AdminUser{}, fmt.Errorf("no owner account: %w", store.ErrNotFound)
}
