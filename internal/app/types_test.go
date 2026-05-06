package app

import (
	"errors"
	"testing"
)

func TestProviderErrorRetryabilityCategories(t *testing.T) {
	retryable := []ProviderErrorKind{ProviderErrorCapacity, ProviderErrorNetwork, ProviderErrorInternal}
	for _, kind := range retryable {
		err := &ProviderError{Kind: kind}
		if !err.Retryable() || !IsRetryableProviderError(err) {
			t.Fatalf("%s should be retryable", kind)
		}
		if got := ProviderErrorKindOf(err); got != kind {
			t.Fatalf("kind = %q, want %q", got, kind)
		}
	}

	terminal := []ProviderErrorKind{
		ProviderErrorAuth,
		ProviderErrorQuota,
		ProviderErrorInvalidSpec,
		ProviderErrorRuntime,
		ProviderErrorUnknown,
	}
	for _, kind := range terminal {
		err := &ProviderError{Kind: kind}
		if err.Retryable() || IsRetryableProviderError(err) {
			t.Fatalf("%s should be terminal", kind)
		}
	}
	if got := ProviderErrorKindOf(errors.New("plain")); got != ProviderErrorUnknown {
		t.Fatalf("plain error kind = %q", got)
	}
}
