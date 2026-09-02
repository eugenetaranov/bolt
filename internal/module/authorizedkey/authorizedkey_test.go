package authorizedkey

import (
	"reflect"
	"testing"
)

const (
	keyA = "ssh-ed25519 AAAAA alice@host"
	keyB = "ssh-rsa BBBBB bob@host"
)

func TestKeyData_IgnoresOptionsAndComment(t *testing.T) {
	// Same key body, different options/comment must compare equal.
	d1 := keyData(`no-pty,command="x" ssh-ed25519 AAAAA alice@laptop`)
	d2 := keyData(`ssh-ed25519 AAAAA alice@server`)
	if d1 != "AAAAA" || d2 != "AAAAA" {
		t.Fatalf("expected both to extract AAAAA, got %q and %q", d1, d2)
	}
	if keyData("not a key line") != "" {
		t.Errorf("expected empty for a non-key line")
	}
}

func TestDesiredLines_PresentAppendsMissing(t *testing.T) {
	got := desiredLines([]string{keyA}, []string{keyB}, "present", false)
	want := []string{keyA, keyB}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("present append = %v, want %v", got, want)
	}
}

func TestDesiredLines_PresentIdempotent(t *testing.T) {
	// Adding a key that's already there (different comment) is a no-op.
	got := desiredLines([]string{keyA}, []string{"ssh-ed25519 AAAAA alice@other"}, "present", false)
	if !reflect.DeepEqual(got, []string{keyA}) {
		t.Errorf("expected unchanged, got %v", got)
	}
}

func TestDesiredLines_Absent(t *testing.T) {
	got := desiredLines([]string{keyA, keyB}, []string{keyA}, "absent", false)
	if !reflect.DeepEqual(got, []string{keyB}) {
		t.Errorf("absent = %v, want %v", got, []string{keyB})
	}
}

func TestDesiredLines_Exclusive(t *testing.T) {
	// Exclusive replaces everything with exactly the given set (deduped).
	got := desiredLines([]string{keyA, keyB}, []string{keyB, "ssh-rsa BBBBB dup"}, "present", true)
	if !reflect.DeepEqual(got, []string{keyB}) {
		t.Errorf("exclusive = %v, want %v", got, []string{keyB})
	}
}

func TestEqualLines(t *testing.T) {
	if !equalLines([]string{keyA}, []string{keyA}) {
		t.Error("expected equal")
	}
	if equalLines([]string{keyA}, []string{keyA, keyB}) {
		t.Error("expected not equal (different length)")
	}
}
