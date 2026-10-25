package s3api_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"ptero-enhanced-backup/gateway/internal/store"
)

func TestDeleteMovesBackupToTrash(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key := "srv/trash.tar.gz"
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	e.api.TrashRetention = 7 * 24 * time.Hour
	e.api.TrashNow = func() time.Time { return now }
	e.backup(key, makeArchive(t, tree{"a.txt": []byte("hello")}, false))

	for range 2 { // deleting again is fine
		if _, err := e.panel.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
			t.Fatalf("DeleteObject: %v", err)
		}
	}
	if len(e.snapshots()) != 1 {
		t.Fatal("the snapshot must stay until the trash is purged")
	}
	if resp, _ := e.download(key); resp.StatusCode != 404 {
		t.Fatalf("download of a trashed backup: %d", resp.StatusCode)
	}
	items, err := e.st.Trash(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("trash: %v %v", items, err)
	}
	if it := items[0]; it.Reason != store.ReasonPanelDelete || !it.PurgeAfter.Equal(now.Add(7*24*time.Hour)) || !it.TrashedAt.Equal(now) {
		t.Fatalf("trash item: %+v", it)
	}
	// Restored backups are served again.
	if err := e.st.RestoreFromTrash(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if resp, body := e.download(key); resp.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("download after restore: %d", resp.StatusCode)
	}
}
