package workspace

import "golang.org/x/crypto/bcrypt"

// HashPassword returns a bcrypt hash for storage in workspace_user.
// password_hash. The plaintext never reaches the database.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword reports whether password matches the stored bcrypt hash.
// An empty hash means password login is not configured — always false.
func CheckPassword(hash, password string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
