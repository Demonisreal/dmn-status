package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestHashVerify(t *testing.T) {
	h := Hash("pferd batterie heftklammer")
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("format %q", h)
	}
	if h == Hash("pferd batterie heftklammer") {
		t.Fatal("gleicher hash zweimal, salt fehlt")
	}

	tests := []struct {
		password string
		ok       bool
	}{
		{"pferd batterie heftklammer", true},
		{"pferd batterie heftklammeR", false},
		{"", false},
		{"pferd batterie heftklammer ", false},
	}
	for _, tt := range tests {
		ok, err := Verify(tt.password, h)
		if err != nil || ok != tt.ok {
			t.Errorf("%q: ok=%v err=%v, want %v", tt.password, ok, err, tt.ok)
		}
	}
}

func TestVerifyOtherParams(t *testing.T) {
	// ein hash mit kleineren parametern muss trotz anderer konstanten pruefbar bleiben
	salt := []byte("saltsaltsalt")
	key := argon2.IDKey([]byte("geheim"), salt, 1, 64, 1, 24)
	h := "$argon2id$v=19$m=64,t=1,p=1$" + b64.EncodeToString(salt) + "$" + b64.EncodeToString(key)

	ok, err := Verify("geheim", h)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestVerifyBadHash(t *testing.T) {
	good := Hash("geheim")
	parts := strings.Split(good, "$")
	with := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, "$")
	}

	tests := map[string]string{
		"leer":              "",
		"bcrypt":            "$2a$10$abcdefghijklmnopqrstuv",
		"argon2i":           with(1, "argon2i"),
		"version":           with(2, "v=16"),
		"parameter fehlen":  with(3, "m=19456,t=2"),
		"muell hinten":      with(3, "m=19456,t=2,p=1x"),
		"reihenfolge":       with(3, "t=2,m=19456,p=1"),
		"speicher riesig":   with(3, "m=4194304,t=2,p=1"),
		"speicher 32 mib+1": with(3, "m=32769,t=2,p=1"),
		"zeit null":         with(3, "m=19456,t=0,p=1"),
		"zeit 6":            with(3, "m=19456,t=6,p=1"),
		"threads 5":         with(3, "m=19456,t=2,p=5"),
		"zu viele threads":  with(3, "m=19456,t=2,p=200"),
		"p ueberlauf":       with(3, "m=19456,t=2,p=257"),
		"negativ":           with(3, "m=-1,t=2,p=1"),
		"salt kein base64":  with(4, "!!!"),
		"salt zu kurz":      with(4, "YWJj"),
		"salt mit padding":  with(4, parts[4]+"=="),
		"hash kein base64":  with(5, "###"),
		"hash zu kurz":      with(5, "YWJjZGVm"),
		"teil zu viel":      good + "$x",
		"ohne fuehrendes $": strings.TrimPrefix(good, "$"),
	}
	for name, h := range tests {
		ok, err := Verify("geheim", h)
		if ok || !errors.Is(err, ErrBadHash) {
			t.Errorf("%s: ok=%v err=%v", name, ok, err)
		}
	}

	// gueltiges format, aber ein bit im hash gekippt
	key, _ := b64.DecodeString(parts[5])
	key[0] ^= 1
	if ok, err := Verify("geheim", with(5, b64.EncodeToString(key))); ok || err != nil {
		t.Error("manipulierter hash akzeptiert")
	}
}

func TestDummyVerify(t *testing.T) {
	if _, err := Verify("x", dummyHash); err != nil {
		t.Fatalf("dummyHash ungueltig: %v", err)
	}
	// aendern sich die konstanten, muss dummyHash neu erzeugt werden, sonst misst er etwas anderes
	parts := strings.Split(dummyHash, "$")
	salt, _ := b64.DecodeString(parts[4])
	key, _ := b64.DecodeString(parts[5])
	if want := fmt.Sprintf("m=%d,t=%d,p=%d", memKiB, passes, threads); parts[3] != want ||
		len(salt) != saltLen || len(key) != keyLen {
		t.Fatalf("dummyHash %s mit salt %d und key %d, want %s mit %d und %d",
			parts[3], len(salt), len(key), want, saltLen, keyLen)
	}

	h := Hash("geheim")
	measure := func(f func()) time.Duration {
		start := time.Now()
		for range 3 {
			f()
		}
		return time.Since(start)
	}
	verify := measure(func() { _, _ = Verify("falsch", h) })
	dummy := measure(func() { DummyVerify("falsch") })
	if dummy < verify/3 {
		t.Fatalf("DummyVerify %v viel schneller als Verify %v", dummy, verify)
	}
}
