package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptedFileStoreRoundTripAndNoPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.enc")
	store, err := OpenEncryptedFile(path, "passphrase")
	if err != nil {
		t.Fatalf("OpenEncryptedFile returned error: %v", err)
	}
	if err := store.Set("lambda/api_key", "secret-value"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(content), "secret-value") {
		t.Fatalf("encrypted store contains plaintext secret: %s", string(content))
	}
	reopened, err := OpenEncryptedFile(path, "passphrase")
	if err != nil {
		t.Fatalf("reopen returned error: %v", err)
	}
	secret, err := reopened.Get("lambda/api_key")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if secret.Value != "secret-value" {
		t.Fatalf("secret = %#v", secret)
	}
	if _, err := OpenEncryptedFile(path, "wrong-passphrase"); err == nil {
		t.Fatal("expected wrong passphrase to fail")
	}
}

func TestResolverUsesStore(t *testing.T) {
	store := NewMemoryStore()
	if err := store.Set("lambda/api_key", "store-value"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	resolver := Resolver{Store: store}
	secret, err := resolver.Resolve(Query{Provider: "lambda", Name: "api-key"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if secret.Value != "store-value" {
		t.Fatalf("store value = %#v", secret)
	}
}

func TestResolverReportsMissingCredential(t *testing.T) {
	_, err := (Resolver{Store: NewMemoryStore()}).Resolve(Query{Provider: "lambda", Name: "api-key"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestKeyNormalizesComponents(t *testing.T) {
	key, err := Key("Lambda", "api-key")
	if err != nil {
		t.Fatalf("Key returned error: %v", err)
	}
	if key != "lambda/api_key" {
		t.Fatalf("key = %q", key)
	}
}
