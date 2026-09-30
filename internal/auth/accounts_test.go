package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juancer/milo/internal/store"
)

func newAccounts(t *testing.T) (*Accounts, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewAccounts(st), st
}

func TestGeneratedPasswordsAreStrongAndReadable(t *testing.T) {
	re := regexp.MustCompile(`^([a-z2-9]{4}-){4}[a-z2-9]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(pw) || strings.ContainsAny(pw, "01lLoOiI") {
			t.Fatalf("password %q has the wrong shape or ambiguous characters", pw)
		}
		if ValidatePassword(pw) != nil {
			t.Fatalf("a generated password fails the password policy: %q", pw)
		}
		if seen[pw] {
			t.Fatalf("duplicate password %q", pw)
		}
		seen[pw] = true
	}
}

const setupPassword = "chosen-by-the-owner-1"

// setupOwner runs the first-run setup as the browser does.
func setupOwner(t *testing.T, a *Accounts) string {
	t.Helper()
	if _, err := a.Setup(context.Background(), "admin", setupPassword); err != nil {
		t.Fatal(err)
	}
	return setupPassword
}

func TestSetupCreatesTheOwnerOnceWithoutForcedChange(t *testing.T) {
	a, st := newAccounts(t)
	ctx := context.Background()
	u, err := a.Setup(ctx, " Admin ", setupPassword)
	if err != nil || u.Username != "admin" || !u.IsOwner || u.MustChange || strings.Contains(u.PasswordHash, setupPassword) {
		t.Fatalf("setup = %+v %v", u, err)
	}
	if _, err := a.Setup(ctx, "other", "another-long-password-1"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("second setup = %v, want ErrSetupDone", err)
	}
	if n, _ := st.AdminCount(ctx); n != 1 {
		t.Fatalf("admins = %d", n)
	}
	if got, err := a.Authenticate(ctx, "admin", setupPassword); err != nil || got.ID != u.ID {
		t.Fatalf("chosen credentials do not work: %v", err)
	}
}

func TestSetupRejectsBadInput(t *testing.T) {
	a, st := newAccounts(t)
	ctx := context.Background()
	if _, err := a.Setup(ctx, "A!", setupPassword); !errors.Is(err, ErrUsername) {
		t.Errorf("bad username = %v", err)
	}
	if _, err := a.Setup(ctx, "admin", "short"); err == nil {
		t.Error("weak password accepted")
	}
	if n, _ := st.AdminCount(ctx); n != 0 {
		t.Fatalf("rejected input created %d admins", n)
	}
}

func TestSetupConcurrentSubmissionsCreateOneOwner(t *testing.T) {
	a, st := newAccounts(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := a.Setup(ctx, fmt.Sprintf("user%d", i), setupPassword); err == nil {
				ok.Add(1)
			} else if !errors.Is(err, ErrSetupDone) && !errors.Is(err, ErrBusy) {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if n, _ := st.AdminCount(ctx); n != 1 || ok.Load() != 1 {
		t.Fatalf("admins = %d, successes = %d; want 1 and 1", n, ok.Load())
	}
}

func TestAuthenticate(t *testing.T) {
	a, _ := newAccounts(t)
	ctx := context.Background()
	pw := setupOwner(t, a)
	if _, err := a.Authenticate(ctx, " ADMIN ", pw); err != nil {
		t.Fatalf("username is case/space insensitive: %v", err)
	}
	for _, c := range [][2]string{{"admin", "wrong-password-here"}, {"nobody", pw}, {"", ""}, {"admin", ""}} {
		if _, err := a.Authenticate(ctx, c[0], c[1]); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("Authenticate(%q, %q) = %v", c[0], c[1], err)
		}
	}
}

func TestCreateAdmins(t *testing.T) {
	a, _ := newAccounts(t)
	ctx := context.Background()
	u, generated, err := a.Create(ctx, "Mom", "")
	if err != nil || u.Username != "mom" || generated == "" || !u.MustChange || u.IsOwner {
		t.Fatalf("Create = %+v %q %v", u, generated, err)
	}
	if _, err := a.Authenticate(ctx, "mom", generated); err != nil {
		t.Fatalf("generated password does not work: %v", err)
	}
	if _, g, err := a.Create(ctx, "dad", "a-chosen-password-1"); err != nil || g != "" {
		t.Fatalf("chosen password: %q %v", g, err)
	}
	if _, _, err := a.Create(ctx, "mom", ""); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate = %v", err)
	}
	for _, bad := range []string{"", "a", "1abc", "with space", "UPPER!", "x/y", strings.Repeat("a", 25), "über"} {
		if _, _, err := a.Create(ctx, bad, ""); !errors.Is(err, ErrUsername) {
			t.Errorf("username %q = %v", bad, err)
		}
	}
	if _, _, err := a.Create(ctx, "kid", "short"); err == nil {
		t.Error("a weak chosen password was accepted")
	}
}

func TestChangeResetAndDelete(t *testing.T) {
	a, st := newAccounts(t)
	ctx := context.Background()
	pw := setupOwner(t, a)
	owner, _ := st.GetAdminByName(ctx, "admin")
	mom, _, _ := a.Create(ctx, "mom", "mom-first-password-1")

	if err := a.ChangePassword(ctx, owner.ID, "wrong", "brand-new-password-1"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong current = %v", err)
	}
	if err := a.ChangePassword(ctx, owner.ID, pw, "short"); err == nil {
		t.Fatal("weak new password accepted")
	}
	if err := a.ChangePassword(ctx, owner.ID, pw, pw); err == nil {
		t.Fatal("re-using the same password accepted")
	}
	if err := a.ChangePassword(ctx, owner.ID, pw, "brand-new-password-1"); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.GetAdmin(ctx, owner.ID); u.MustChange {
		t.Fatal("must_change not cleared after choosing a password")
	}
	if _, err := a.Authenticate(ctx, "admin", pw); err == nil {
		t.Fatal("old password still works")
	}

	fresh, err := a.Reset(ctx, mom.ID)
	if err != nil || fresh == "" {
		t.Fatal(err)
	}
	if u, _ := st.GetAdmin(ctx, mom.ID); !u.MustChange {
		t.Fatal("a reset password must be changed at next sign-in")
	}
	if _, err := a.Authenticate(ctx, "mom", "mom-first-password-1"); err == nil {
		t.Fatal("old password works after reset")
	}
	if _, err := a.Authenticate(ctx, "mom", fresh); err != nil {
		t.Fatal(err)
	}

	if err := a.Delete(ctx, mom.ID, mom.ID); !errors.Is(err, ErrSelf) {
		t.Fatalf("self delete = %v", err)
	}
	if err := a.Delete(ctx, mom.ID, owner.ID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("owner delete = %v", err)
	}
	if err := a.Delete(ctx, owner.ID, mom.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, owner.ID, mom.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
	if o, err := a.Owner(ctx); err != nil || o.Username != "admin" {
		t.Fatalf("Owner = %+v %v", o, err)
	}
}

func TestPasswordCheckGivesUpWhenAllKDFSlotsAreHeld(t *testing.T) {
	a, _ := newAccounts(t)
	pw := setupOwner(t, a)
	for i := 0; i < cap(kdfSlots); i++ {
		kdfSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(kdfSlots); i++ {
			<-kdfSlots
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyPasswordContext(ctx, pw, a.dummyHash); !errors.Is(err, ErrBusy) {
		t.Fatalf("VerifyPasswordContext with cancelled ctx = %v, want ErrBusy", err)
	}
	old := kdfWait
	kdfWait = time.Millisecond
	defer func() { kdfWait = old }()
	if _, err := a.Authenticate(context.Background(), "admin", pw); !errors.Is(err, ErrBusy) {
		t.Fatalf("Authenticate with no free slot = %v, want ErrBusy", err)
	}
}
