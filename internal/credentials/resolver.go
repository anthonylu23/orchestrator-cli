package credentials

import (
	"fmt"
	"os"
)

type Query struct {
	Provider string
	Name     string
	Env      []string
}

type Resolver struct {
	Store Store
}

func (r Resolver) Resolve(query Query) (Secret, error) {
	key, err := Key(query.Provider, query.Name)
	if err != nil {
		return Secret{}, err
	}
	for _, envName := range query.Env {
		if value := os.Getenv(envName); value != "" {
			return Secret{Key: key, Value: value}, nil
		}
	}
	if r.Store != nil {
		secret, err := r.Store.Get(key)
		if err == nil {
			return secret, nil
		}
		if err != ErrNotFound {
			return Secret{}, err
		}
	}
	return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, key)
}
