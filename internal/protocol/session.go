package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
)

func isValidAESKeyLen(n int) bool {
	return n == 16 || n == 24 || n == 32
}

// GenerateRSAKeyPair generates an RSA private key with the given bit size.
func GenerateRSAKeyPair(bits int) (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, bits)
}

// DecryptAESKeyOAEP decrypts an RSA-OAEP (SHA-256) encrypted AES key using the private key.
func DecryptAESKeyOAEP(priv *rsa.PrivateKey, encKey []byte) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("nil private key")
	}
	if len(encKey) != priv.Size() {
		return nil, errors.New("invalid encrypted key length")
	}
	key, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, encKey, nil)
	if err != nil {
		return nil, err
	}
	if !isValidAESKeyLen(len(key)) {
		return nil, fmt.Errorf("invalid AES key length %d: must be 16, 24, or 32", len(key))
	}
	return key, nil
}

// DecryptAESKey decrypts an RSA encrypted AES key using the private key.
// It attempts RSA-OAEP (SHA-256) first, falling back to PKCS#1 v1.5 session key decryption.
func DecryptAESKey(priv *rsa.PrivateKey, encKey []byte) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("nil private key")
	}
	if len(encKey) != priv.Size() {
		return nil, errors.New("invalid encrypted key length")
	}
	// Try RSA-OAEP SHA-256 first
	if key, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, encKey, nil); err == nil {
		if !isValidAESKeyLen(len(key)) {
			return nil, fmt.Errorf("invalid AES key length %d: must be 16, 24, or 32", len(key))
		}
		return key, nil
	}
	sessionKey := make([]byte, 32)
	if _, err := rand.Read(sessionKey); err != nil {
		return nil, fmt.Errorf("generate fallback session key: %w", err)
	}
	//nolint:staticcheck // GitHub Actions broker protocol supports PKCS#1 v1.5 session key exchange
	if err := rsa.DecryptPKCS1v15SessionKey(rand.Reader, priv, encKey, sessionKey); err != nil {
		return nil, err
	}
	return sessionKey, nil
}

// DecryptPayload decrypts AES-CBC encrypted ciphertext using the provided AES key and IV,
// then removes PKCS#7 padding in constant time.
func DecryptPayload(aesKey, iv, ciphertext []byte) ([]byte, error) {
	if !isValidAESKeyLen(len(aesKey)) {
		return nil, fmt.Errorf("invalid AES key length %d: must be 16, 24, or 32", len(aesKey))
	}
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
	out := make([]byte, len(b)+padding)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(padding)
	}
	return out
}

func pkcs7Unpad(b []byte, blockSize int) ([]byte, error) {
	if blockSize <= 0 || blockSize > 255 || len(b) == 0 || len(b)%blockSize != 0 {
		return nil, errors.New("decryption failed")
	}

	padByte := b[len(b)-1]
	padLen := int(padByte)

	valid := subtle.ConstantTimeLessOrEq(1, padLen)
	valid &= subtle.ConstantTimeLessOrEq(padLen, blockSize)

	start := len(b) - blockSize
	for i := 0; i < blockSize; i++ {
		inPad := subtle.ConstantTimeLessOrEq(blockSize-i, padLen)
		byteEq := subtle.ConstantTimeByteEq(b[start+i], padByte)
		valid &= subtle.ConstantTimeSelect(inPad, byteEq, 1)
	}

	if valid != 1 {
		return nil, errors.New("decryption failed")
	}
	return b[:len(b)-padLen], nil
}
