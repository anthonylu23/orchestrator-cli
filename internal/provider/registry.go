package provider

import (
	"fmt"
	"sort"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

type Registry struct {
	providers map[app.ProviderName]app.ProviderAdapter
}

func NewRegistry(adapters ...app.ProviderAdapter) *Registry {
	reg := &Registry{providers: map[app.ProviderName]app.ProviderAdapter{}}
	for _, adapter := range adapters {
		reg.providers[adapter.Name()] = adapter
	}
	return reg
}

func (r *Registry) Get(name string) (app.ProviderAdapter, error) {
	adapter, ok := r.providers[app.ProviderName(name)]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return adapter, nil
}

func (r *Registry) List() []app.ProviderName {
	names := make([]app.ProviderName, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}
