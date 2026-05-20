package credentials

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func PassphraseFromEnvOrPrompt(prompt string, stderr io.Writer) (string, error) {
	if value := os.Getenv(PassphraseEnv); value != "" {
		return value, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("%s is required in non-interactive mode", PassphraseEnv)
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprint(stderr, prompt)
	content, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return "", errors.New("credentials passphrase must not be empty")
	}
	return value, nil
}

func SecretFromPrompt(prompt string, stderr io.Writer) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("secret value source is required in non-interactive mode")
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	_, _ = fmt.Fprint(stderr, prompt)
	content, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return "", errors.New("secret value must not be empty")
	}
	return value, nil
}
