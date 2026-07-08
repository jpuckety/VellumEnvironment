package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// KeySize is the required size for AES-256 (32 bytes).
	KeySize = 32
	// NonceSize for GCM.
	NonceSize = 12
)

var (
	ErrInvalidKey        = errors.New("invalid master key length: must be 32 bytes")
	ErrInvalidCiphertext = errors.New("invalid ciphertext format")
	ErrDecryptionFailed  = errors.New("decryption failed")
)

// Service provides AES-256-GCM encryption and decryption.
type Service struct {
	key [KeySize]byte
}

// New creates a new crypto service from a 32-byte key.
func New(key []byte) (*Service, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	var k [KeySize]byte
	copy(k[:], key)
	return &Service{key: k}, nil
}

// NewFromEnv creates a crypto service using EMAILMCP_MASTER_KEY.
// The value must be a base64-encoded 32-byte key.
func NewFromEnv() (*Service, error) {
	encoded := os.Getenv("EMAILMCP_MASTER_KEY")
	if encoded == "" {
		return nil, errors.New("EMAILMCP_MASTER_KEY environment variable is required")
	}

	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("failed to base64 decode EMAILMCP_MASTER_KEY: %w", err)
	}

	return New(key)
}

// MustNewFromEnv panics if the key cannot be loaded. Useful for startup.
func MustNewFromEnv() *Service {
	svc, err := NewFromEnv()
	if err != nil {
		panic(fmt.Sprintf("crypto initialization failed: %v", err))
	}
	return svc
}

// Encrypt encrypts plaintext using AES-256-GCM.
// Returns base64-encoded (nonce || ciphertext).
func (s *Service) Encrypt(plaintext []byte) (string, error) {
	if len(plaintext) == 0 {
		return "", nil
	}

	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a base64-encoded (nonce || ciphertext) value.
func (s *Service) Decrypt(ciphertextB64 string) ([]byte, error) {
	if ciphertextB64 == "" {
		return nil, nil
	}

	data, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCiphertext, err)
	}

	if len(data) < NonceSize {
		return nil, ErrInvalidCiphertext
	}

	nonce := data[:NonceSize]
	ciphertext := data[NonceSize:]

	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecryptionFailed, err)
	}

	return plaintext, nil
}

// EncryptString is a convenience wrapper for string input.
func (s *Service) EncryptString(plaintext string) (string, error) {
	return s.Encrypt([]byte(plaintext))
}

// DecryptString decrypts and returns a UTF-8 string.
func (s *Service) DecryptString(ciphertextB64 string) (string, error) {
	b, err := s.Decrypt(ciphertextB64)
	if err != nil {
		return "", err
	}
	if b == nil {
		return "", nil
	}
	return string(b), nil
}
