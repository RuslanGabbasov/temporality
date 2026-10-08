package workspace

import "testing"

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" || hash == "correct horse battery staple" {
		t.Fatalf("hash must be non-empty and not the plaintext, got %q", hash)
	}
	if !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("CheckPassword must accept the original password")
	}
	if CheckPassword(hash, "wrong password") {
		t.Fatal("CheckPassword must reject a wrong password")
	}
	if CheckPassword("", "anything") {
		t.Fatal("an empty hash means password login is not configured")
	}
}

func TestHashPasswordSalts(t *testing.T) {
	first, err := HashPassword("same input")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword("same input")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Fatal("bcrypt hashes must be salted — identical inputs give different hashes")
	}
}
