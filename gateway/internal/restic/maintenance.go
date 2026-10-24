package restic

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"ptero-enhanced-backup/gateway/internal/gate"
)

// RepoStats is the subset of `restic stats --mode raw-data --json` we use.
type RepoStats struct {
	TotalSize             int64   `json:"total_size"`
	TotalUncompressedSize int64   `json:"total_uncompressed_size"`
	CompressionRatio      float64 `json:"compression_ratio"`
	SnapshotsCount        int64   `json:"snapshots_count"`
}

// PruneResult is what could be read from the prune output.
type PruneResult struct {
	BytesFreed int64 // -1 if the output could not be parsed
	Output     string
}

// Unlock removes stale locks only (never --remove-all).
func (c *Client) Unlock(ctx context.Context) error { return c.run(ctx, "unlock") }

// Reachable checks that the repository can be opened.
func (c *Client) Reachable(ctx context.Context) error { return c.run(ctx, "cat", "config") }

// Prune removes unused data; it needs the exclusive repository lock.
func (c *Client) Prune(ctx context.Context, maxUnused string) (PruneResult, error) {
	args := []string{"prune"}
	if maxUnused != "" {
		args = append(args, "--max-unused", maxUnused)
	}
	out, err := c.output(ctx, args...)
	return PruneResult{BytesFreed: parsePruneFreed(string(out)), Output: string(out)}, err
}

// Check verifies the repository structure; readDataSubset (e.g. "5%") also reads packs.
func (c *Client) Check(ctx context.Context, readDataSubset string) error {
	args := []string{"check"}
	if readDataSubset != "" {
		args = append(args, "--read-data-subset", readDataSubset)
	}
	return c.run(ctx, args...)
}

// RawStats returns the repository size as stored (after compression and dedup).
func (c *Client) RawStats(ctx context.Context) (RepoStats, error) {
	var s RepoStats
	out, err := c.output(ctx, "stats", "--mode", "raw-data", "--json")
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(out, &s)
	return s, err
}

var pruneTotalRe = regexp.MustCompile(`(?m)^total prune:\s+\d+ blobs / ([\d.]+) (B|KiB|MiB|GiB|TiB)\s*$`)

// parsePruneFreed reads the "total prune" line of the text output.
func parsePruneFreed(out string) int64 {
	m := pruneTotalRe.FindStringSubmatch(out)
	if m == nil {
		return -1
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return -1
	}
	mult := map[string]float64{"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[m[2]]
	return int64(v * mult)
}

// Short returns the last non-empty line of s, for one-line job messages.
func Short(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// RepoType returns the backend of a repository string: rest, sftp, s3, rclone,
// b2, azure, gs, swift or local.
func RepoType(repo string) string {
	for _, p := range []string{"rest", "sftp", "s3", "rclone", "b2", "azure", "gs", "swift"} {
		if strings.HasPrefix(repo, p+":") {
			return p
		}
	}
	return "local"
}

// FSSpace reports the size of the filesystem holding path and the space available
// to unprivileged processes (f_bavail), in bytes.
func FSSpace(path string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := int64(st.Bsize)
	return int64(st.Blocks) * bs, int64(st.Bavail) * bs, nil
}

// DirSize sums the file sizes below dir (the real on-disk size of a local repository).
func DirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished while walking (concurrent prune)
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n, err
}

// Gated runs the restic commands that touch the repository outside the
// ingest/dump paths only while the gate is open, so they never race with
// maintenance (prune, check). Prune, Check and Unlock are not gated: the
// maintenance service pauses the gate itself.
type Gated struct {
	*Client
	Gate *gate.Gate
}

// Forget waits for the gate (bounded by ctx), then forgets the snapshot.
func (g *Gated) Forget(ctx context.Context, snapshotID string) error {
	if err := g.Gate.Enter(ctx); err != nil {
		return err
	}
	defer g.Gate.Leave()
	return g.Client.Forget(ctx, snapshotID)
}

// RawStats waits for the gate (bounded by ctx), then reads the repository stats.
func (g *Gated) RawStats(ctx context.Context) (RepoStats, error) {
	if err := g.Gate.Enter(ctx); err != nil {
		return RepoStats{}, err
	}
	defer g.Gate.Leave()
	return g.Client.RawStats(ctx)
}
