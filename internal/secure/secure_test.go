package secure

import (
	"bytes"
	"strings"
	"testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
	password := "correct horse battery staple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("hash does not use Argon2id: %q", hash)
	}
	if strings.Contains(hash, password) {
		t.Fatal("password hash contains the plaintext password")
	}
	if !VerifyPassword(hash, password) {
		t.Fatal("VerifyPassword() rejected the correct password")
	}
	if VerifyPassword(hash, "incorrect horse battery staple") {
		t.Fatal("VerifyPassword() accepted an incorrect password")
	}

	for _, malformed := range []string{
		"", "$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=16$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=2$not-base64$not-base64",
	} {
		if VerifyPassword(malformed, password) {
			t.Errorf("VerifyPassword(%q, password) unexpectedly succeeded", malformed)
		}
	}
}

func TestPasswordLengthValidation(t *testing.T) {
	if _, err := HashPassword("too-short"); err == nil {
		t.Fatal("HashPassword() accepted a password shorter than 12 bytes")
	}
	if _, err := HashPassword(strings.Repeat("x", 129)); err == nil {
		t.Fatal("HashPassword() accepted a password longer than 128 bytes")
	}
}

func TestBoxAuthenticatesCiphertextAndContext(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	box, err := NewBox(key)
	if err != nil {
		t.Fatalf("NewBox() error = %v", err)
	}
	plain := []byte("admin-api-key-plaintext-sentinel")
	ciphertext, err := box.Seal(plain, "settings:v1")
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if strings.Contains(ciphertext, string(plain)) {
		t.Fatal("ciphertext contains plaintext")
	}
	opened, err := box.Open(ciphertext, "settings:v1")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("Open() = %q, want %q", opened, plain)
	}

	if _, err := box.Open(ciphertext, "different-context"); err == nil {
		t.Fatal("Open() accepted ciphertext with the wrong AAD")
	}
	wrongBox, err := NewBox(bytes.Repeat([]byte{0x22}, 32))
	if err != nil {
		t.Fatalf("NewBox(wrong key) error = %v", err)
	}
	if _, err := wrongBox.Open(ciphertext, "settings:v1"); err == nil {
		t.Fatal("Open() accepted ciphertext with the wrong master key")
	}

	secondCiphertext, err := box.Seal(plain, "settings:v1")
	if err != nil {
		t.Fatalf("second Seal() error = %v", err)
	}
	if ciphertext == secondCiphertext {
		t.Fatal("Seal() reused a nonce for identical plaintext")
	}
	if _, err := NewBox(make([]byte, 31)); err == nil {
		t.Fatal("NewBox() accepted a non-256-bit key")
	}
}

func TestBoxRejectsMalformedCiphertext(t *testing.T) {
	box, err := NewBox(bytes.Repeat([]byte{0x33}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"not-base64!", "", "YWJjZA"} {
		if _, err := box.Open(value, "settings:v1"); err == nil {
			t.Errorf("Open(%q) unexpectedly succeeded", value)
		}
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty key", value: "", want: ""},
		{name: "short key", value: "short", want: "…hort"},
		{name: "trimmed short key", value: "  short  ", want: "…hort"},
		{name: "very short Sub2API key", value: "sk-x", want: "sk-…"},
		{name: "Sub2API key", value: "sk-abcdefghijklmnopqrstuvwxyz", want: "sk-…wxyz"},
		{name: "custom key", value: "abcdefghijklmnop", want: "…mnop"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MaskAPIKey(test.value); got != test.want {
				t.Fatalf("MaskAPIKey(%q) = %q, want %q", test.value, got, test.want)
			}
			trimmed := strings.TrimSpace(test.value)
			if len(trimmed) >= 12 && strings.Contains(MaskAPIKey(test.value), trimmed) {
				t.Fatal("masked key contains the complete key")
			}
		})
	}
}

func TestRedactRemovesKnownSecretForms(t *testing.T) {
	secrets := []string{
		"admin-super-secret-token",
		"sk-1234567890abcdef",
		"api-key-value",
		"password-value",
		"generic-key-value",
	}
	input := "request failed: admin-super-secret-token sk-1234567890abcdef " +
		`api_key="api-key-value" password=password-value key=generic-key-value request_id=req-123`
	redacted := Redact(input)
	for _, secret := range secrets {
		if strings.Contains(redacted, secret) {
			t.Errorf("Redact() leaked %q: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "request_id=req-123") {
		t.Fatalf("Redact() removed non-sensitive context: %s", redacted)
	}
	if strings.Count(redacted, "[REDACTED]") < 5 {
		t.Fatalf("Redact() did not replace all secret forms: %s", redacted)
	}
}
