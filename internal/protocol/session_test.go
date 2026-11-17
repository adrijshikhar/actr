package protocol

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"testing"
)

func TestCryptoRoundtrip(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate RSA: %v", err)
	}

	aesKey := make([]byte, 32) // AES-256
	if _, err := rand.Read(aesKey); err != nil {
		t.Fatalf("rand read aes key: %v", err)
	}

	// Encrypt AES key using RSA public key
	//nolint:staticcheck // GitHub Actions broker protocol requires PKCS#1 v1.5 session key exchange
	encAESKey, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, aesKey)
	if err != nil {
		t.Fatalf("rsa encrypt: %v", err)
	}

	decAESKey, err := DecryptAESKey(key, encAESKey)
	if err != nil {
		t.Fatalf("decrypt aes key: %v", err)
	}
	if !bytes.Equal(aesKey, decAESKey) {
		t.Fatal("decrypted AES key does not match original")
	}

	plaintext := []byte("hello github actions payload")
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand read iv: %v", err)
	}

	// Encrypt plaintext with AES-CBC + PKCS7 padding
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	enc := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(padded))
	enc.CryptBlocks(ciphertext, padded)

	decrypted, err := DecryptPayload(aesKey, iv, ciphertext)
	if err != nil {
		t.Fatalf("decrypt payload: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("expected '%s', got '%s'", plaintext, decrypted)
	}
}

func TestDecryptPayload_InvalidInputs(t *testing.T) {
	aesKey := make([]byte, 32)
	iv := make([]byte, aes.BlockSize)

	// Invalid key size
	if _, err := DecryptPayload([]byte("short-key"), iv, make([]byte, 16)); err == nil {
		t.Error("expected error for invalid key size, got nil")
	}

	// Ciphertext not multiple of block size
	if _, err := DecryptPayload(aesKey, iv, make([]byte, 15)); err == nil {
		t.Error("expected error for ciphertext not multiple of block size, got nil")
	}

	// Invalid IV size
	if _, err := DecryptPayload(aesKey, []byte("short-iv"), make([]byte, 16)); err == nil {
		t.Error("expected error for invalid IV size, got nil")
	}

	// Empty ciphertext
	if _, err := DecryptPayload(aesKey, iv, []byte{}); err == nil {
		t.Error("expected error for empty ciphertext, got nil")
	}

	// Invalid padding
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	enc := cipher.NewCBCEncrypter(block, iv)
	invalidPad := []byte("123456789012345\x00") // 0 is invalid padding value in PKCS7
	ciphertext := make([]byte, len(invalidPad))
	enc.CryptBlocks(ciphertext, invalidPad)

	if _, err := DecryptPayload(aesKey, iv, ciphertext); err == nil {
		t.Error("expected error for invalid padding byte 0, got nil")
	}

	// Inconsistent padding bytes
	inconsistentPad := []byte("12345678901234\x01\x02")
	ciphertext = make([]byte, len(inconsistentPad))
	enc = cipher.NewCBCEncrypter(block, iv)
	enc.CryptBlocks(ciphertext, inconsistentPad)

	if _, err := DecryptPayload(aesKey, iv, ciphertext); err == nil {
		t.Error("expected error for inconsistent padding bytes, got nil")
	}
}

func TestDecryptAESKey_InvalidCiphertext(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate RSA: %v", err)
	}

	if _, err := DecryptAESKey(key, []byte("invalid-encrypted-key")); err == nil {
		t.Error("expected error decrypting invalid key, got nil")
	}

	if _, err := DecryptAESKey(nil, []byte("some-key")); err == nil {
		t.Error("expected error decrypting with nil key, got nil")
	}
}

func TestCryptoRoundtrip_OAEP(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate RSA: %v", err)
	}

	aesKey := make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		t.Fatalf("rand read aes key: %v", err)
	}

	// Encrypt AES key using RSA-OAEP SHA-256
	encAESKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, aesKey, nil)
	if err != nil {
		t.Fatalf("rsa encrypt OAEP: %v", err)
	}

	// Decrypt using DecryptAESKeyOAEP
	decAESKey, err := DecryptAESKeyOAEP(key, encAESKey)
	if err != nil {
		t.Fatalf("decrypt aes key OAEP: %v", err)
	}
	if !bytes.Equal(aesKey, decAESKey) {
		t.Fatal("decrypted AES key (OAEP) does not match original")
	}

	// Decrypt using DecryptAESKey (should auto-detect OAEP)
	decAESKeyFallback, err := DecryptAESKey(key, encAESKey)
	if err != nil {
		t.Fatalf("decrypt aes key via DecryptAESKey: %v", err)
	}
	if !bytes.Equal(aesKey, decAESKeyFallback) {
		t.Fatal("decrypted AES key via DecryptAESKey does not match original")
	}
}

func TestDecryptAESKeyOAEP_Invalid(t *testing.T) {
	key, err := GenerateRSAKeyPair(2048)
	if err != nil {
		t.Fatalf("generate RSA: %v", err)
	}

	if _, err := DecryptAESKeyOAEP(key, []byte("invalid-oaep-ciphertext")); err == nil {
		t.Error("expected error decrypting invalid OAEP ciphertext, got nil")
	}

	if _, err := DecryptAESKeyOAEP(nil, []byte("some-data")); err == nil {
		t.Error("expected error with nil private key, got nil")
	}
}
