package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	envelopeVersion = 1
	documentVersion = 1
	kdfName         = "argon2id"
	cipherName      = "aes-256-gcm"
)

type KDFParams struct {
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
	KeyLen    uint32 `json:"key_len"`
}

type Envelope struct {
	Version    int       `json:"version"`
	KDF        string    `json:"kdf"`
	KDFParams  KDFParams `json:"kdf_params"`
	Cipher     string    `json:"cipher"`
	Salt       string    `json:"salt"`
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
}

type EncryptedFileStore struct {
	path       string
	passphrase string
	doc        Document
	now        func() time.Time
}

func DefaultPath(home string) string {
	return filepath.Join(home, FileName)
}

func OpenEncryptedFile(path string, passphrase string) (*EncryptedFileStore, error) {
	store := &EncryptedFileStore{
		path:       path,
		passphrase: passphrase,
		doc:        Document{Version: documentVersion, Items: map[string]Item{}},
		now:        func() time.Time { return time.Now().UTC() },
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials store: %w", err)
	}
	if len(content) == 0 {
		return store, nil
	}
	doc, err := decryptDocument(content, passphrase)
	if err != nil {
		return nil, err
	}
	if doc.Items == nil {
		doc.Items = map[string]Item{}
	}
	store.doc = doc
	return store, nil
}

func (s *EncryptedFileStore) Init(force bool) error {
	if !force {
		if _, err := os.Stat(s.path); err == nil {
			return fmt.Errorf("credentials store already exists: %s", s.path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.save()
}

func (s *EncryptedFileStore) Get(key string) (Secret, error) {
	if _, _, err := SplitKey(key); err != nil {
		return Secret{}, err
	}
	item, ok := s.doc.Items[key]
	if !ok {
		return Secret{}, ErrNotFound
	}
	return Secret{Key: key, Value: item.Value, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}, nil
}

func (s *EncryptedFileStore) Set(key string, value string) error {
	if _, _, err := SplitKey(key); err != nil {
		return err
	}
	if value == "" {
		return errors.New("credential value must not be empty")
	}
	now := s.now()
	item := s.doc.Items[key]
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	item.Value = value
	s.doc.Items[key] = item
	return s.save()
}

func (s *EncryptedFileStore) Delete(key string) error {
	if _, _, err := SplitKey(key); err != nil {
		return err
	}
	if _, ok := s.doc.Items[key]; !ok {
		return ErrNotFound
	}
	delete(s.doc.Items, key)
	return s.save()
}

func (s *EncryptedFileStore) List() ([]Metadata, error) {
	out := make([]Metadata, 0, len(s.doc.Items))
	for key, item := range s.doc.Items {
		provider, name, err := SplitKey(key)
		if err != nil {
			return nil, err
		}
		out = append(out, Metadata{Key: key, Provider: provider, Name: name, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *EncryptedFileStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	content, err := encryptDocument(s.doc, s.passphrase)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create credentials temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace credentials store: %w", err)
	}
	return os.Chmod(s.path, 0o600)
}

func encryptDocument(doc Document, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("credentials passphrase is required")
	}
	plain, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	params := KDFParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4, KeyLen: 32}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	key := deriveKey(passphrase, salt, params)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	envelope := Envelope{
		Version:    envelopeVersion,
		KDF:        kdfName,
		KDFParams:  params,
		Cipher:     cipherName,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, plain, nil)),
	}
	return json.MarshalIndent(envelope, "", "  ")
}

func decryptDocument(content []byte, passphrase string) (Document, error) {
	if passphrase == "" {
		return Document{}, errors.New("credentials passphrase is required")
	}
	var envelope Envelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return Document{}, fmt.Errorf("parse credentials store: %w", err)
	}
	if envelope.Version != envelopeVersion || envelope.KDF != kdfName || envelope.Cipher != cipherName {
		return Document{}, errors.New("unsupported credentials store format")
	}
	salt, err := base64.StdEncoding.DecodeString(envelope.Salt)
	if err != nil {
		return Document{}, fmt.Errorf("decode credentials salt: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return Document{}, fmt.Errorf("decode credentials nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return Document{}, fmt.Errorf("decode credentials ciphertext: %w", err)
	}
	key := deriveKey(passphrase, salt, envelope.KDFParams)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Document{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Document{}, err
	}
	plain, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return Document{}, errors.New("decrypt credentials store: invalid passphrase or corrupted store")
	}
	var doc Document
	if err := json.Unmarshal(plain, &doc); err != nil {
		return Document{}, fmt.Errorf("parse decrypted credentials: %w", err)
	}
	if doc.Version != documentVersion {
		return Document{}, errors.New("unsupported credentials document version")
	}
	return doc, nil
}

func deriveKey(passphrase string, salt []byte, params KDFParams) []byte {
	if params.Time == 0 {
		params.Time = 3
	}
	if params.MemoryKiB == 0 {
		params.MemoryKiB = 64 * 1024
	}
	if params.Threads == 0 {
		params.Threads = 4
	}
	if params.KeyLen == 0 {
		params.KeyLen = 32
	}
	return argon2.IDKey([]byte(passphrase), salt, params.Time, params.MemoryKiB, params.Threads, params.KeyLen)
}
