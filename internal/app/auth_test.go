package app

import "testing"

func TestTokenHashIsStableAndNonEmpty(t *testing.T) {
	if tokenHash("example-secret") != tokenHash("example-secret") {
		t.Fatal("hash is not stable")
	}
	if tokenHash("example-secret") == tokenHash("different-secret") {
		t.Fatal("hash collision in test inputs")
	}
}

func TestSubtleCompare(t *testing.T) {
	if !subtleCompare("abc", "abc") || subtleCompare("abc", "abd") || subtleCompare("abc", "abcd") {
		t.Fatal("constant-time comparison result incorrect")
	}
}
