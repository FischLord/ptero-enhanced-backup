package restic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"ptero-enhanced-backup/gateway/internal/secrets"
)

// Key is one entry of `restic key list`.
type Key struct {
	ID      string    `json:"id"`
	User    string    `json:"userName"`
	Host    string    `json:"hostName"`
	Created time.Time `json:"-"`
	Current bool      `json:"current"`
	Raw     string    `json:"created"`
}

// keyLockWait is how long key add/remove wait for the repository lock. restic
// fails at once when another restic process (a running backup, a stats run)
// holds a lock; key remove needs the exclusive lock, so without waiting it
// fails whenever the gateway is busy.
const keyLockWait = "2m"

var keyIDRe = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

// ErrCurrentKey is returned when the key in use would be removed.
var ErrCurrentKey = errors.New("refusing to remove the key in use")

// KeyAdd adds a second password to the repository and returns the new key ID.
// The password goes to restic through a temporary 0600 file that is removed
// again; it never appears in the process list, in a log or in an error text.
func (c *Client) KeyAdd(ctx context.Context, newPassword, host, user string) (string, error) {
	dir := c.CacheDir
	if dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	f, err := os.CreateTemp(dir, ".peb-newkey-*") // created with mode 0600
	if err != nil {
		return "", errors.New("restic key add: cannot create temporary password file")
	}
	name := f.Name()
	defer os.Remove(name)
	_ = f.Chmod(0o600)
	_, werr := f.WriteString(newPassword)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return "", errors.New("restic key add: cannot write temporary password file")
	}
	args := []string{"key", "add", "--retry-lock", keyLockWait, "--new-password-file", name}
	if host != "" {
		args = append(args, "--host", host)
	}
	if user != "" {
		args = append(args, "--user", user)
	}
	out, err := c.keyOutput(ctx, args...)
	if err != nil {
		return "", c.scrubKeyErr(err, newPassword, name)
	}
	id := keyIDRe.FindString(string(out))
	if id == "" {
		return "", errors.New("restic key add: no key ID in output")
	}
	return id, nil
}

// KeyList lists the keys of the repository.
func (c *Client) KeyList(ctx context.Context) ([]Key, error) {
	out, err := c.output(ctx, "key", "list", "--no-lock", "--json")
	if err != nil {
		return nil, c.scrubKeyErr(err)
	}
	var keys []Key
	if err := json.Unmarshal(out, &keys); err != nil {
		return nil, fmt.Errorf("restic key list: %w", err)
	}
	for i := range keys {
		// restic prints the local time of the creating machine without a zone.
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", keys[i].Raw, time.Local); err == nil {
			keys[i].Created = t
		}
	}
	return keys, nil
}

// KeyRemove deletes a key. It refuses to remove the key the client itself uses.
func (c *Client) KeyRemove(ctx context.Context, id string) error {
	keys, err := c.KeyList(ctx)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.ID == id && k.Current {
			return ErrCurrentKey
		}
	}
	if _, err := c.keyOutput(ctx, "key", "remove", "--retry-lock", keyLockWait, id); err != nil {
		return c.scrubKeyErr(err)
	}
	return nil
}

// isLockErr reports a failure caused by another lock in the repository.
func isLockErr(err error) bool {
	m := err.Error()
	return strings.Contains(m, "already locked") || strings.Contains(m, "unable to create lock")
}

// keyOutput runs a key add/remove. If it still fails on a lock after
// --retry-lock, the lock may be left over from a killed restic (a live one would
// have been waited out), so it runs `restic unlock` (stale locks only, the same
// call maintenance uses) and tries once more.
func (c *Client) keyOutput(ctx context.Context, args ...string) ([]byte, error) {
	out, err := c.output(ctx, args...)
	if err == nil || !isLockErr(err) {
		return out, err
	}
	if uerr := c.Unlock(ctx); uerr != nil {
		return out, err
	}
	return c.output(ctx, args...)
}

func (c *Client) scrubKeyErr(err error, known ...string) error {
	known = append(known, c.Password, c.FromPassword, c.Repo)
	return errors.New(secrets.ScrubWith(err.Error(), known...))
}
