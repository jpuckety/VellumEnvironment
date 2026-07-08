package crypto

import (
	"encoding/base64"
	"testing"
)

func TestNewInvalidKey(t *testing.T) {
	_, err := New([]byte("short"))
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}

	svc, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	plaintext := "sensitive-password-123!@#"
	enc, err := svc.EncryptString(plaintext)
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}

	if enc == "" {
		t.Fatal("expected non-empty ciphertext")
	}

	dec, err := svc.DecryptString(enc)
	if err != nil {
		t.Fatalf("DecryptString: %v", err)
	}

	if dec != plaintext {
		t.Errorf("roundtrip mismatch: got %q want %q", dec, plaintext)
	}
}

func TestDecryptInvalid(t *testing.T) {
	key := make([]byte, KeySize)
	svc, _ := New(key)

	_, err := svc.Decrypt("not-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}

	// valid base64 but too short
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	_, err = svc.Decrypt(short)
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestEmpty(t *testing.T) {
	key := make([]byte, KeySize)
	svc, _ := New(key)

	enc, _ := svc.EncryptString("")
	if enc != "" {
		t.Error("empty should encrypt to empty")
	}

	dec, _ := svc.DecryptString("")
	if dec != "" {
		t.Error("empty should decrypt to empty")
	}
}
