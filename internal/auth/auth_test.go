package auth

import (
	"strconv"
	"strings"
	"sync"
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

// Try counts the attempt in the same step as the check, so a burst cannot all get through.
func TestLimiterTryAdmitsAtMostMaxAttemptsInAParallelBurst(t *testing.T) {
	l := NewLimiter(5, time.Minute)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Try("ip") {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 5 {
		t.Fatalf("admitted %d attempts, want 5", admitted)
	}
	l.Reset("ip")
	if !l.Try("ip") {
		t.Fatal("still blocked after Reset")
	}
}

func TestVerifyRejectsOutOfRangeStoredParameters(t *testing.T) {
	for _, params := range []string{"m=4294967295,t=2,p=1", "m=19456,t=0,p=1", "m=19456,t=2,p=0", "m=0,t=2,p=1", "m=19456,t=1000,p=1", "m=19456,t=2,p=255"} {
		h := "$argon2id$v=19$" + params + "$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNo"
		if ok, err := VerifyPassword("x", h); ok || err == nil {
			t.Errorf("VerifyPassword with %s = %v, %v; want error", params, ok, err)
		}
	}
}

func TestLimiterSweepsKeysThatAreNeverAskedAgain(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	l.Fail("a")
	l.Fail("b")
	now = now.Add(2 * time.Minute)
	l.Fail("c")
	if len(l.fails) != 1 {
		t.Fatalf("stale keys kept: %d", len(l.fails))
	}
}

func TestLimiterRefundKeepsEarlierFailures(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	l.Fail("ip")
	l.Fail("ip")
	if !l.Try("ip") { // third slot, then the attempt succeeds
		t.Fatal("blocked too early")
	}
	l.Refund("ip")
	if !l.Try("ip") || l.Try("ip") {
		t.Fatal("Refund must give back exactly one attempt and keep the two earlier failures")
	}
	l.Refund("gone")
}

func TestLimiterMapIsCapped(t *testing.T) {
	l := NewLimiter(5, time.Minute)
	for i := 0; i < maxKeys+500; i++ {
		l.Try(strconv.Itoa(i))
		l.Fail("f" + strconv.Itoa(i))
	}
	if len(l.fails) > maxKeys {
		t.Fatalf("fails map grew to %d, cap %d", len(l.fails), maxKeys)
	}
}
