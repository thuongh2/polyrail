package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// Box encrypts secrets at rest with AES-256-GCM. The key is derived from the operator's MASTER_KEY.
type Box struct{ aead cipher.AEAD }

func NewBox(masterKey string) (*Box, error) {
	if len(masterKey) < 16 {
		return nil, errors.New("MASTER_KEY must be at least 16 characters")
	}
	// The label keeps the pre-rename name on purpose: changing it would make stored secrets unreadable (D24).
	k := sha256.Sum256([]byte("agent-gateway/secret/v1:" + masterKey))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (b *Box) Open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", errors.New("invalid sealed secret")
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("cannot decrypt secret (wrong MASTER_KEY?)")
	}
	return string(plain), nil
}
