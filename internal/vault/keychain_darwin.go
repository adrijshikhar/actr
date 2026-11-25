//go:build darwin

package vault

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type DarwinKeychainProvider struct {
	service string
}

func NewKeychainProvider(serviceName string) Provider {
	if serviceName == "" {
		serviceName = "com.github.actr.secrets"
	}
	return &DarwinKeychainProvider{service: serviceName}
}

func (k *DarwinKeychainProvider) Name() string {
	return "keychain"
}

func (k *DarwinKeychainProvider) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// #nosec G204 -- /usr/bin/security is hardcoded and arguments are passed as discrete parameters without shell evaluation
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", k.service, "-a", key, "-w")
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrSecretNotFound
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

func (k *DarwinKeychainProvider) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// If value, key, or service contains newlines, security -i cannot be used because
	// newlines act as command delimiters in interactive mode. In that case, fall back to direct argv execution.
	if strings.ContainsAny(value, "\r\n") || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(k.service, "\r\n") {
		// #nosec G204 -- /usr/bin/security is hardcoded and arguments are passed as discrete parameters without shell evaluation
		cmd := exec.CommandContext(ctx, "/usr/bin/security", "add-generic-password", "-U", "-s", k.service, "-a", key, "-w", value)
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("keychain set %s: %w (%s)", key, err, strings.TrimSpace(errBuf.String()))
		}
		return nil
	}

	// #nosec G204 -- /usr/bin/security is hardcoded and arguments are passed via stdin in interactive mode
	// so sensitive secrets are never exposed in process argv (e.g. ps aux, sysctl KERN_PROCARGS2).
	escapedVal := strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "\"", "\\\"")
	escapedKey := strings.ReplaceAll(strings.ReplaceAll(key, "\\", "\\\\"), "\"", "\\\"")
	escapedSvc := strings.ReplaceAll(strings.ReplaceAll(k.service, "\\", "\\\\"), "\"", "\\\"")

	input := fmt.Sprintf("add-generic-password -U -s \"%s\" -a \"%s\" -w \"%s\"\n", escapedSvc, escapedKey, escapedVal)

	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(input)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("keychain set %s: %w (%s)", key, err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

func (k *DarwinKeychainProvider) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// #nosec G204 -- /usr/bin/security is hardcoded and arguments are passed as discrete parameters without shell evaluation
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", k.service, "-a", key)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrSecretNotFound
	}
	return nil
}

func (k *DarwinKeychainProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "/usr/bin/security", "dump-keychain")
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}

	return parseDumpKeychain(string(out), k.service, k.Name()), nil
}

func parseDumpKeychain(out string, targetService, providerName string) []SecretMetadata {
	var results []SecretMetadata
	seen := make(map[string]bool)
	lines := strings.Split(out, "\n")
	var currentService, currentAccount string

	flush := func() {
		if currentService == targetService && currentAccount != "" && currentAccount != "<NULL>" {
			if !seen[currentAccount] {
				seen[currentAccount] = true
				results = append(results, SecretMetadata{
					Key:      currentAccount,
					Provider: providerName,
				})
			}
		}
		currentService = ""
		currentAccount = ""
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "keychain: ") || strings.HasPrefix(line, "class: ") {
			flush()
			continue
		}
		if strings.HasPrefix(line, "\"svce\"<blob>=") {
			val := strings.TrimPrefix(line, "\"svce\"<blob>=")
			currentService = strings.Trim(val, "\"")
		} else if strings.HasPrefix(line, "\"acct\"<blob>=") {
			val := strings.TrimPrefix(line, "\"acct\"<blob>=")
			currentAccount = strings.Trim(val, "\"")
		}
	}
	flush()
	return results
}
