package cli

import "testing"

func TestBootProfile_Has(t *testing.T) {
	tests := []struct {
		name string
		p    BootProfile
		f    BootProfile
		want bool
	}{
		{"empty has nothing", 0, NeedStore, false},
		{"single bit", NeedStore, NeedStore, true},
		{"single bit misses other", NeedStore, NeedCipher, false},
		{"composite has subset", NeedStore | NeedCipher, NeedStore, true},
		{"composite has self", NeedStore | NeedCipher, NeedStore | NeedCipher, true},
		{"composite misses missing bit", NeedStore | NeedCipher, NeedZitadel, false},
		{"all profiles has each", AllProfiles, NeedStore, true},
		{"all profiles has cipher", AllProfiles, NeedCipher, true},
		{"all profiles has signer", AllProfiles, NeedSigner, true},
		{"all profiles has zitadel", AllProfiles, NeedZitadel, true},
		{"all profiles has oidcrp", AllProfiles, NeedOIDCRP, true},
		{"all profiles has upstream", AllProfiles, NeedUpstream, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Has(tt.f); got != tt.want {
				t.Errorf("(%b).Has(%b) = %v, want %v", tt.p, tt.f, got, tt.want)
			}
		})
	}
}

// TestAllProfiles_IsUnion guards against forgetting to fold a new
// BootProfile flag into AllProfiles. If you add a Need* constant, add
// it to this list and to the AllProfiles definition.
func TestAllProfiles_IsUnion(t *testing.T) {
	all := []BootProfile{
		NeedStore, NeedCipher, NeedSigner, NeedZitadel, NeedOIDCRP, NeedUpstream,
	}
	var union BootProfile
	for _, f := range all {
		union |= f
	}
	if AllProfiles != union {
		t.Fatalf("AllProfiles (%b) != union of known flags (%b) — did you forget to fold a new flag in?", AllProfiles, union)
	}
}
