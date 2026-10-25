// Package restic wraps the external restic binary.
package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// cancelSignal is sent to restic when its context ends; killDelay is how long
// it then gets to remove its repository lock before it is killed.
const (
	cancelSignal = syscall.SIGTERM
	killDelay    = 15 * time.Second
)

// Client runs restic against one repository.
type Client struct {
	Bin          string
	Repo         string
	Password     string
	PasswordFile string
	CacheDir     string
	// TmpDir is where restic writes temporary pack files (TMPDIR). Wings mounts
	// /tmp as a 100 MB tmpfs, too small for a copy. An operator-set TMPDIR wins.
	TmpDir string

	// Source repository for copy and init --copy-chunker-params (passed by
	// environment, so credentials never show up in the process list).
	FromRepo         string
	FromPassword     string
	FromPasswordFile string
}

// Summary is the final "summary" message of `restic backup --json`.
type Summary struct {
	SnapshotID          string `json:"snapshot_id"`
	DataAdded           int64  `json:"data_added"`
	DataAddedPacked     int64  `json:"data_added_packed"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	TotalFilesProcessed int64  `json:"total_files_processed"`
	FilesNew            int64  `json:"files_new"`
	FilesChanged        int64  `json:"files_changed"`
	FilesUnmodified     int64  `json:"files_unmodified"`
}

// BackupOptions describes one stdin backup.
type BackupOptions struct {
	Filename string
	Host     string
	Tags     []string
}

// Snapshot is the subset of `restic snapshots --json` we use.
type Snapshot struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags"`
	// Original is the ID of the source snapshot if this one was made by `restic copy`.
	Original string `json:"original"`
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	env := cleanEnv(os.Environ())
	env = append(env, "RESTIC_REPOSITORY="+c.Repo, "RESTIC_PROGRESS_FPS=0.1")
	if c.FromRepo != "" {
		env = append(env, "RESTIC_FROM_REPOSITORY="+c.FromRepo)
	}
	if c.FromPasswordFile != "" {
		env = append(env, "RESTIC_FROM_PASSWORD_FILE="+c.FromPasswordFile)
	}
	if c.FromPassword != "" {
		env = append(env, "RESTIC_FROM_PASSWORD="+c.FromPassword)
	}
	if c.PasswordFile != "" {
		env = append(env, "RESTIC_PASSWORD_FILE="+c.PasswordFile)
	}
	if c.Password != "" {
		env = append(env, "RESTIC_PASSWORD="+c.Password)
	}
	if c.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+c.CacheDir)
	}
	if c.TmpDir != "" && os.Getenv("TMPDIR") == "" {
		env = append(env, "TMPDIR="+c.TmpDir)
	}
	cmd.Env = env
	// Ask restic to shut down cleanly (removes its repository lock) before killing it.
	cmd.Cancel = func() error { return cmd.Process.Signal(cancelSignal) }
	cmd.WaitDelay = killDelay
	return cmd
}

// cleanEnv drops the restic repository and password variables of the process, so
// every Client uses exactly its own settings (a second repository must not
// inherit the first one's password).
func cleanEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "RESTIC_REPOSITORY", "RESTIC_REPOSITORY_FILE", "RESTIC_PASSWORD", "RESTIC_PASSWORD_FILE", "RESTIC_PASSWORD_COMMAND",
			"RESTIC_FROM_REPOSITORY", "RESTIC_FROM_REPOSITORY_FILE", "RESTIC_FROM_PASSWORD", "RESTIC_FROM_PASSWORD_FILE", "RESTIC_FROM_PASSWORD_COMMAND":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// CleanTmp removes restic-temp-pack-* files left in dir by an interrupted run.
// Only call it while no restic process is running. Returns the number removed.
func CleanTmp(dir string) int {
	files, _ := filepath.Glob(filepath.Join(dir, "restic-temp-pack-*"))
	n := 0
	for _, f := range files {
		if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() && os.Remove(f) == nil {
			n++
		}
	}
	return n
}

func (c *Client) bin() string {
	if c.Bin == "" {
		return "restic"
	}
	return c.Bin
}

// Init creates the repository if it does not exist yet.
func (c *Client) Init(ctx context.Context) error {
	if err := c.run(ctx, "cat", "config"); err == nil {
		return nil
	}
	return c.run(ctx, "init")
}

func (c *Client) run(ctx context.Context, args ...string) error {
	_, err := c.output(ctx, args...)
	return err
}

func (c *Client) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := c.command(ctx, args...)
	var out bytes.Buffer
	var errb limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("restic %s: %w: %s", args[0], err, errb.String())
	}
	return out.Bytes(), nil
}

// Backup streams r into a new snapshot. If restic reports an incomplete
// snapshot it exits non-zero; the returned Summary may then still carry a
// SnapshotID that the caller should forget.
func (c *Client) Backup(ctx context.Context, r io.Reader, o BackupOptions) (Summary, error) {
	args := []string{"backup", "--stdin", "--stdin-filename", o.Filename, "--json"}
	if o.Host != "" {
		args = append(args, "--host", o.Host)
	}
	for _, t := range o.Tags {
		args = append(args, "--tag", t)
	}
	cmd := c.command(ctx, args...)
	cmd.Stdin = r
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Summary{}, err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Summary{}, err
	}
	var sum Summary
	var jsonErrs []string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var msg struct {
			MessageType string `json:"message_type"`
			Error       struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		line := sc.Bytes()
		if json.Unmarshal(line, &msg) != nil {
			continue
		}
		switch msg.MessageType {
		case "summary":
			_ = json.Unmarshal(line, &sum)
		case "error":
			jsonErrs = append(jsonErrs, msg.Error.Message)
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return sum, ctx.Err()
		}
		return sum, fmt.Errorf("restic backup: %w: %s %s", err, strings.Join(jsonErrs, "; "), stderr.String())
	}
	if sum.SnapshotID == "" {
		return sum, errors.New("restic backup: no summary in output")
	}
	return sum, nil
}

// Forget removes a snapshot (without pruning).
// A snapshot that is already gone counts as success.
func (c *Client) Forget(ctx context.Context, snapshotID string) error {
	err := c.run(ctx, "forget", snapshotID)
	if err != nil && isNoSnapshot(err) {
		return nil
	}
	return err
}

// ForgetRaw is Forget for callers that hold the gate themselves (maintenance).
// Gated.Forget would wait for the paused gate and never return.
func (c *Client) ForgetRaw(ctx context.Context, snapshotID string) error {
	return c.Forget(ctx, snapshotID)
}

func isNoSnapshot(err error) bool {
	m := err.Error()
	return strings.Contains(m, "no matching ID") || strings.Contains(m, "could not find snapshot") ||
		strings.Contains(m, "no snapshot found")
}

// Snapshots lists the snapshots in the repository.
func (c *Client) Snapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := c.output(ctx, "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}

// Dump streams a file of a snapshot. A restic failure shows up as a read
// error instead of io.EOF, so truncated output is never mistaken for complete.
func (c *Client) Dump(ctx context.Context, snapshotID, path string) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := c.command(ctx, "dump", snapshotID, path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	d := &dumpReader{cmd: cmd, out: out, cancel: cancel}
	cmd.Stderr = &d.stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	return d, nil
}

type dumpReader struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	cancel context.CancelFunc
	stderr limitedBuffer
	once   sync.Once
	err    error
}

func (d *dumpReader) wait() error {
	d.once.Do(func() {
		if err := d.cmd.Wait(); err != nil {
			d.err = fmt.Errorf("restic dump: %w: %s", err, d.stderr.String())
		}
	})
	return d.err
}

func (d *dumpReader) Read(p []byte) (int, error) {
	n, err := d.out.Read(p)
	if err == io.EOF {
		if werr := d.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func (d *dumpReader) Close() error {
	// Closing the pipe first lets restic stop on EPIPE instead of waiting out the kill delay.
	_ = d.out.Close()
	d.cancel()
	_ = d.wait()
	return nil
}

// limitedBuffer keeps the first 8 KiB of stderr for error messages.
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := 8192 - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buf.String())
}
