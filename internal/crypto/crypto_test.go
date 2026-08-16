package crypto

import (
	"encoding/base64"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(make([]byte, KeySize))
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := testKey(t)
	plain := "sk-very-secret-upstream-key"
	enc, err := Encrypt(plain, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if enc == plain {
		t.Fatal("ciphertext must differ from plaintext")
	}
	dec, err := Decrypt(enc, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if dec != plain {
		t.Fatalf("roundtrip mismatch: got %q want %q", dec, plain)
	}
}

func TestEncryptEmptyOrNoKeyPassthrough(t *testing.T) {
	key := testKey(t)
	if v, err := Encrypt("", key); err != nil || v != "" {
		t.Fatalf("empty plaintext should passthrough: %q %v", v, err)
	}
	if v, err := Encrypt("abc", ""); err != nil || v != "abc" {
		t.Fatalf("no key should passthrough: %q %v", v, err)
	}
}

func TestDecryptLegacyPlaintextPassthrough(t *testing.T) {
	key := testKey(t)
	legacy := "legacy-plaintext-credential"
	v, err := Decrypt(legacy, key)
	if err != nil || v != legacy {
		t.Fatalf("legacy plaintext must passthrough: %q %v", v, err)
	}
	if v, err := Decrypt(legacy, ""); err != nil || v != legacy {
		t.Fatalf("legacy plaintext without key must passthrough: %q %v", v, err)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	key1 := testKey(t)
	other := base64.StdEncoding.EncodeToString(append(make([]byte, 31), 1))
	enc, err := Encrypt("secret", key1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(enc, other); err == nil {
		t.Fatal("decrypt with wrong key must fail")
	}
}

func TestDecryptEncryptedWithoutKeyFails(t *testing.T) {
	enc, err := Encrypt("secret", testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(enc, ""); err == nil {
		t.Fatal("encrypted value without key must fail")
	}
}

func TestDecodeKeyValidation(t *testing.T) {
	if _, err := DecodeKey(""); err == nil {
		t.Fatal("empty key must fail")
	}
	if _, err := DecodeKey("not-base64!!"); err == nil {
		t.Fatal("invalid base64 must fail")
	}
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := DecodeKey(short); err == nil {
		t.Fatal("16-byte key must fail (want 32)")
	}
}

func TestMaskSuffix(t *testing.T) {
	if got := MaskSuffix("sk-abcdefgh-1234"); got != "****1234" {
		t.Fatalf("MaskSuffix = %q", got)
	}
	if got := MaskSuffix("short"); got != "****" {
		t.Fatalf("MaskSuffix short = %q", got)
	}
	if got := MaskSuffix(""); got != "" {
		t.Fatalf("MaskSuffix empty = %q", got)
	}
}
