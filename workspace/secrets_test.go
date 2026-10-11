package workspace

import (
	"bytes"
	"testing"
)

func TestParseSecretReference(t *testing.T) {
	cases := []struct {
		value    string
		wantOK   bool
		wantName string
		wantKey  string
	}{
		{"secret://github-token", true, "github-token", ""},
		{"secret://gh/token", false, "", ""},
		{"secret://", false, "", ""},
		{"secret://#", false, "", ""},
		{"github-token", false, "", ""},
		{"", false, "", ""},
		{"secret://db.json#password", true, "db.json", "password"},
		{"secret://creds#nested:key", true, "creds", "nested:key"},
	}
	for _, tc := range cases {
		ref, ok := ParseSecretReference(tc.value)
		if ok != tc.wantOK {
			t.Errorf("ParseSecretReference(%q) ok = %v, want %v", tc.value, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if ref.Name != tc.wantName || ref.Key != tc.wantKey {
			t.Errorf("ParseSecretReference(%q) = %q/%q, want %q/%q", tc.value, ref.Name, ref.Key, tc.wantName, tc.wantKey)
		}
	}
}

func TestSecretSealOpenRoundtrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	ciphertext, nonce, err := SealSecret(key, []byte("ghp_supersensitive"))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if bytes.Contains(ciphertext, []byte("supersensitive")) {
		t.Fatal("ciphertext must not contain plaintext")
	}
	plain, err := OpenSecret(key, ciphertext, nonce)
	if err != nil {
		t.Fatalf("OpenSecret: %v", err)
	}
	if string(plain) != "ghp_supersensitive" {
		t.Fatalf("roundtrip got %q", plain)
	}
}

func TestSecretOpenRejectsWrongKey(t *testing.T) {
	ciphertext, nonce, err := SealSecret(bytes.Repeat([]byte{7}, 32), []byte("value"))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if _, err := OpenSecret(bytes.Repeat([]byte{8}, 32), ciphertext, nonce); err == nil {
		t.Fatal("OpenSecret must fail with a wrong master key")
	}
}
