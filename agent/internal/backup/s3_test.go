package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
)

func TestS3StorePutStatDownloadAndDelete(t *testing.T) {
	var mu sync.Mutex
	var object []byte
	var metadata http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=access/") || strings.Contains(r.Header.Get("Authorization"), "secret") {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			object, _ = io.ReadAll(r.Body)
			metadata = r.Header.Clone()
			w.Header().Set("ETag", `"etag-1"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			if object == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			copyObjectHeaders(w.Header(), metadata, len(object))
		case http.MethodGet:
			if object == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			copyObjectHeaders(w.Header(), metadata, len(object))
			_, _ = w.Write(object)
		case http.MethodDelete:
			object = nil
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	store, err := NewS3Store(backupv1.StoreSpec{ID: "store-1", Provider: backupv1.StoreProviderS3, Endpoint: server.URL, Bucket: "bucket", Region: "test-1", AllowInsecure: true}, backupv1.StoreCredential{AccessKey: "access", SecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("logical backup artifact")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	info, err := store.Put(context.Background(), "projects/p/backups/b.dump", bytes.NewReader(data), int64(len(data)), sha, "bkp-1")
	if err != nil || info.ETag != "etag-1" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	stat, err := store.Stat(context.Background(), "projects/p/backups/b.dump")
	if err != nil || stat.Size != int64(len(data)) || stat.SHA256 != sha || stat.BackupID != "bkp-1" {
		t.Fatalf("stat=%+v err=%v", stat, err)
	}
	body, _, err := store.Get(context.Background(), "projects/p/backups/b.dump")
	if err != nil {
		t.Fatal(err)
	}
	downloaded, _ := io.ReadAll(body)
	body.Close()
	if !bytes.Equal(downloaded, data) {
		t.Fatal("download mismatch")
	}
	if err := store.Delete(context.Background(), "projects/p/backups/b.dump"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(context.Background(), "projects/p/backups/b.dump"); err != ErrObjectNotFound {
		t.Fatalf("stat after delete=%v", err)
	}
}

func TestS3StorePutDoesNotCloseCallerFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, err := NewS3Store(backupv1.StoreSpec{ID: "store-1", Provider: backupv1.StoreProviderS3, Endpoint: server.URL, Bucket: "bucket", Region: "test-1", AllowInsecure: true}, backupv1.StoreCredential{AccessKey: "access", SecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "artifact-*.dump")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data := []byte("logical backup")
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if _, err := store.Put(context.Background(), "backup.dump", file, int64(len(data)), hex.EncodeToString(sum[:]), "bkp-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("caller staging file was closed: %v", err)
	}
}

func copyObjectHeaders(target, source http.Header, size int) {
	target.Set("Content-Length", strconv.Itoa(size))
	target.Set("ETag", `"etag-1"`)
	target.Set("x-amz-meta-sha256", source.Get("x-amz-meta-sha256"))
	target.Set("x-amz-meta-opsi-backup-id", source.Get("x-amz-meta-opsi-backup-id"))
}

func TestS3StoreRealMinIO(t *testing.T) {
	endpoint, access, secret, bucket := os.Getenv("OPSI_E2E_MINIO_ENDPOINT"), os.Getenv("OPSI_E2E_MINIO_ACCESS_KEY"), os.Getenv("OPSI_E2E_MINIO_SECRET_KEY"), os.Getenv("OPSI_E2E_MINIO_BUCKET")
	if endpoint == "" || access == "" || secret == "" || bucket == "" {
		t.Skip("set disposable MinIO endpoint, credentials, and bucket")
	}
	store, err := NewS3Store(backupv1.StoreSpec{ID: "minio-e2e", Provider: backupv1.StoreProviderS3, Endpoint: endpoint, Bucket: bucket, Region: "us-east-1", AllowInsecure: true}, backupv1.StoreCredential{AccessKey: access, SecretKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	request, err := store.request(ctx, http.MethodPut, "", nil, emptySHA256, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict {
		t.Fatalf("create MinIO bucket status=%d", response.StatusCode)
	}
	data := []byte("real S3-compatible logical backup artifact")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	key := "provider-tests/" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".dump"
	if _, err := store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), sha, "bkp-minio"); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx, key)
	if err != nil || info.Size != int64(len(data)) || info.SHA256 != sha || info.BackupID != "bkp-minio" {
		t.Fatalf("stat=%+v err=%v", info, err)
	}
	body, _, err := store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, readErr := io.ReadAll(body)
	body.Close()
	if readErr != nil || !bytes.Equal(downloaded, data) {
		t.Fatalf("download mismatch err=%v", readErr)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
}
