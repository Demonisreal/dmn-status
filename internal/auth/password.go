// Package auth enthaelt Passwort-Hashing fuer den Admin-Login.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// owasp-empfehlung fuer argon2id: 19 MiB, 2 durchlaeufe, 1 thread
const (
	memKiB  = 19456
	passes  = 2
	threads = 1
	saltLen = 16
	keyLen  = 32
)

var ErrBadHash = errors.New("passwort-hash ungueltig")

var b64 = base64.RawStdEncoding.Strict()

// Hash liefert einen PHC-String wie $argon2id$v=19$m=19456,t=2,p=1$salt$hash.
func Hash(password string) string {
	salt := make([]byte, saltLen)
	rand.Read(salt)
	key := argon2.IDKey([]byte(password), salt, passes, memKiB, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memKiB, passes, threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// Verify vergleicht in konstanter Zeit. Die Parameter kommen aus dem String, alte Hashes
// bleiben also gueltig, wenn sich die Konstanten oben aendern.
func Verify(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, ErrBadHash
	}

	var m, t uint32
	var p uint8
	// Sscanf ignoriert angehaengten muell, der vergleich mit der neu formatierten form nicht
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil ||
		fmt.Sprintf("m=%d,t=%d,p=%d", m, t, p) != parts[3] {
		return false, ErrBadHash
	}
	// obergrenzen, damit ein manipulierter hash in der datenbank den login nicht lahmlegt
	if m < 8 || m > 32<<10 || t < 1 || t > 5 || p < 1 || p > 4 {
		return false, ErrBadHash
	}

	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, ErrBadHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return false, ErrBadHash
	}

	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(key))) //nolint:gosec // len(key) ist oben auf 16..64 begrenzt
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// fest eingebaut statt beim start berechnet, sonst waere der erste fehlversuch langsamer
const dummyHash = "$argon2id$v=19$m=19456,t=2,p=1$eMChjSfz96mvasvn2JDzWg$p7siaGhJhOm6Y70NEtq54KgArBJ2PnRa83vSoAywPes"

// DummyVerify kostet so viel Zeit wie Verify mit den Standardparametern. Fuer unbekannte
// Benutzernamen, damit die Antwortzeit nicht verraet, ob es den Namen gibt.
func DummyVerify(password string) {
	_, _ = Verify(password, dummyHash)
}
