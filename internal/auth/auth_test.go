package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") || strings.Contains(h, "correct horse") {
		t.Fatalf("bad hash format: %s", h)
	}
	if ok, err := VerifyPassword("correct horse battery", h); !ok || err != nil {
		t.Fatalf("verify good = %v, %v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong password!!", h); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{"", "plaintext", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=x$AA$AA", "$argon2id$v=19$m=1,t=1,p=1$!!$AA"} {
		if ok, err := VerifyPassword("x", bad); ok || err == nil {
			t.Errorf("VerifyPassword(%q) = %v, %v; want error", bad, ok, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if ValidatePassword("short") == nil {
		t.Fatal("short password accepted")
	}
	if ValidatePassword(strings.Repeat("a", 200)) == nil {
		t.Fatal("huge password accepted")
	}
	if err := ValidatePassword("long enough pw"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsExpireAndBindToUserAndPassword(t *testing.T) {
	s := NewSessions(time.Hour)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }

	tok, err := s.Create(7, Fingerprint("hash-1"))
	if err != nil {
		t.Fatal(err)
	}
	uid, fp, ok := s.Lookup(tok)
	if !ok || uid != 7 || !Matches(fp, "hash-1") {
		t.Fatalf("lookup = %d %v %v", uid, fp, ok)
	}
	if Matches(fp, "hash-2") {
		t.Fatal("a session must not match another password")
	}
	now = now.Add(2 * time.Hour)
	if _, _, ok := s.Lookup(tok); ok {
		t.Fatal("expired session accepted")
	}
	for _, bad := range []string{"", "unknown"} {
		if _, _, ok := s.Lookup(bad); ok {
			t.Fatalf("bogus token %q accepted", bad)
		}
	}
	tok, _ = s.Create(7, "x")
	s.Delete(tok)
	if _, _, ok := s.Lookup(tok); ok {
		t.Fatal("deleted session accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.Allowed("ip") {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("ip")
	}
	if l.Allowed("ip") {
		t.Fatal("not blocked after max failures")
	}
	if !l.Allowed("other") {
		t.Fatal("other key affected")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allowed("ip") {
		t.Fatal("still blocked after window")
	}
	l.Fail("ip")
	l.Reset("ip")
	if len(l.fails) != 0 {
		t.Fatal("Reset left state")
	}
}
