package restic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// ExitRepoMissing is restic's exit code for "repository does not exist" (restic >= 0.17).
const ExitRepoMissing = 10

// Exists reports whether the repository can be opened. A missing repository
// gives (false, nil); every other failure (network, credentials, wrong
// password) is returned as an error and must not lead to an init.
func (c *Client) Exists(ctx context.Context) (bool, error) {
	_, err := c.output(ctx, "cat", "config")
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == ExitRepoMissing {
		return false, nil
	}
	return false, err
}

// InitCopy creates the repository with the chunker parameters of the source
// repository (FromRepo), so that copied data stays deduplicated.
func (c *Client) InitCopy(ctx context.Context) error {
	if c.FromRepo == "" {
		return errors.New("no source repository set")
	}
	return c.run(ctx, "init", "--copy-chunker-params")
}

// Copy copies the snapshots of FromRepo that are missing in this repository.
// restic recognises already copied snapshots itself.
func (c *Client) Copy(ctx context.Context) error {
	if c.FromRepo == "" {
		return errors.New("no source repository set")
	}
	return c.run(ctx, "copy")
}

// ChunkerPolynomial returns the chunker polynomial from the repository config.
func (c *Client) ChunkerPolynomial(ctx context.Context) (string, error) {
	out, err := c.output(ctx, "cat", "config")
	if err != nil {
		return "", err
	}
	var cfg struct {
		Poly string `json:"chunker_polynomial"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return "", fmt.Errorf("restic cat config: %w", err)
	}
	return cfg.Poly, nil
}

// RepoID returns the repository ID from `restic cat config`.
func (c *Client) RepoID(ctx context.Context) (string, error) {
	out, err := c.output(ctx, "cat", "config")
	if err != nil {
		return "", err
	}
	var cfg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return "", fmt.Errorf("restic cat config: %w", err)
	}
	if cfg.ID == "" {
		return "", errors.New("restic cat config: no repository id")
	}
	return cfg.ID, nil
}

// ForgetMany removes the snapshots in one call (without pruning).
func (c *Client) ForgetMany(ctx context.Context, ids []string) error {
	for len(ids) > 0 {
		n := min(len(ids), 100)
		if err := c.run(ctx, append([]string{"forget"}, ids[:n]...)...); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}
