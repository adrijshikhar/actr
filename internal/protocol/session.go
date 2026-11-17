package protocol

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"fmt"
)

// GenerateRSAKeyPair generates an RSA private key with the given bit size.
func GenerateRSAKeyPair(bits int) (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, bits)
}

// DecryptAESKeyOAEP decrypts an RSA-OAEP (SHA-256) encrypted AES key using the private key.
func DecryptAESKeyOAEP(priv *rsa.PrivateKey, encKey []byte) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("nil private key")
	}
	return rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, encKey, nil)
}

// DecryptAESKey decrypts an RSA encrypted AES key using the private key.
// It attempts RSA-OAEP (SHA-256) first, falling back to PKCS#1 v1.5.
func DecryptAESKey(priv *rsa.PrivateKey, encKey []byte) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("nil private key")
	}
	// Try RSA-OAEP SHA-256 first
	if key, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, encKey, nil); err == nil {
		return key, nil
	}
	//nolint:staticcheck // GitHub Actions broker protocol supports PKCS#1 v1.5 session key exchange
	return rsa.DecryptPKCS1v15(rand.Reader, priv, encKey)
}

// DecryptPayload decrypts AES-CBC encrypted ciphertext using the provided AES key and IV,
// then removes PKCS#7 padding.
func DecryptPayload(aesKey, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext is not a multiple of the block size")
	}
	if len(iv) != aes.BlockSize {
		return nil, errors.New("iv length must equal block size")
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	decrypted := make([]byte, len(ciphertext))
	mode.CryptBlocks(decrypted, ciphertext)

	return pkcs7Unpad(decrypted, aes.BlockSize)
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	padding := blockSize - (len(b) % blockSize)
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(b, padtext...)
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty buffer")
	}
	padding := int(b[len(b)-1])
	if padding == 0 || padding > blockSize || padding > len(b) {
		return nil, errors.New("invalid padding")
	}
	for i := len(b) - padding; i < len(b); i++ {
		if b[i] != byte(padding) {
			return nil, errors.New("invalid padding byte")
		}
	}
	return b[:len(b)-padding], nil
}
