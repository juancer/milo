package main

import "testing"

func TestDevPasswordIsRandomUnlessGiven(t *testing.T) {
	t.Setenv("MILO_DEV_PASSWORD", "")
	a, err := devPassword()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := devPassword()
	if len(a) < 16 || a == b || a == "milo-demo-password" {
		t.Fatalf("passwords %q and %q are not random", a, b)
	}
	t.Setenv("MILO_DEV_PASSWORD", "chosen-by-me-123")
	if got, _ := devPassword(); got != "chosen-by-me-123" {
		t.Fatalf("got %q, want the one from the environment", got)
	}
}
