package controller

import (

	"golang.org/x/crypto/bcrypt"
)

const (
	// PasswordHashFile is the key in the environment SSH Secret holding the
	// bcrypt hash of the dev user's password. Empty disables password login.
	PasswordHashFile = "password_hash"

	bcryptCost = 10
)

// MinPasswordLength is enforced by the API.
const MinPasswordLength = 8

// HashPassword returns a bcrypt hash suitable for /etc/shadow and the gateway.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(b), err
}
