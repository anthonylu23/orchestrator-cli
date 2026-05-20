package credentials

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	PassphraseEnv = "SWITCHBOARD_CREDENTIALS_PASSPHRASE"
	FileName      = "credentials.enc"
)

var (
	ErrNotFound       = errors.New("credential not found")
	ErrInvalidKey     = errors.New("invalid credential key")
	validKeyComponent = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)
)

type Secret struct {
	Key       string
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Metadata struct {
	Key       string    `json:"key"`
	Provider  string    `json:"provider"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Item struct {
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Document struct {
	Version int             `json:"version"`
	Items   map[string]Item `json:"items"`
}

type Store interface {
	Get(key string) (Secret, error)
	Set(key string, value string) error
	Delete(key string) error
	List() ([]Metadata, error)
}

func Key(provider string, name string) (string, error) {
	provider = normalizeComponent(provider)
	name = normalizeComponent(name)
	if !validKeyComponent.MatchString(provider) || !validKeyComponent.MatchString(name) {
		return "", fmt.Errorf("%w: %q/%q", ErrInvalidKey, provider, name)
	}
	return provider + "/" + name, nil
}

func SplitKey(key string) (string, string, error) {
	provider, name, ok := strings.Cut(key, "/")
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	canonical, err := Key(provider, name)
	if err != nil {
		return "", "", err
	}
	if canonical != key {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return provider, name, nil
}

func normalizeComponent(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "-", "_")
	return value
}
