package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
)

var ErrObjectNotFound = errors.New("backup object not found")

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type ObjectInfo struct {
	Size      int64
	SHA256    string
	BackupID  string
	ETag      string
	VersionID string
}

type ObjectStore interface {
	Put(context.Context, string, io.ReadSeeker, int64, string, string) (ObjectInfo, error)
	Stat(context.Context, string) (ObjectInfo, error)
	Get(context.Context, string) (io.ReadCloser, ObjectInfo, error)
	Delete(context.Context, string) error
}

type S3Store struct {
	Spec       backupv1.StoreSpec
	Credential backupv1.StoreCredential
	HTTPClient *http.Client
	Now        func() time.Time
}

func NewS3Store(spec backupv1.StoreSpec, credential backupv1.StoreCredential) (*S3Store, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := credential.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if spec.CABundlePEM != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM([]byte(spec.CABundlePEM)) {
			return nil, errors.New("backup store CA bundle is invalid")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &S3Store{Spec: spec, Credential: credential, HTTPClient: &http.Client{Transport: transport, Timeout: 30 * time.Minute}}, nil
}

func (s *S3Store) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sha, backupID string) (ObjectInfo, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return ObjectInfo{}, err
	}
	// net/http closes request bodies; keep ownership of the caller's staging file.
	req, err := s.request(ctx, http.MethodPut, key, io.NopCloser(body), sha, map[string]string{
		"content-type":              "application/octet-stream",
		"x-amz-meta-opsi-backup-id": backupID,
		"x-amz-meta-sha256":         sha,
	})
	if err != nil {
		return ObjectInfo{}, err
	}
	req.ContentLength = size
	resp, err := s.client().Do(req)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ObjectInfo{}, responseError("upload backup object", resp)
	}
	return ObjectInfo{Size: size, SHA256: sha, BackupID: backupID, ETag: strings.Trim(resp.Header.Get("ETag"), "\""), VersionID: resp.Header.Get("x-amz-version-id")}, nil
}

func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	req, err := s.request(ctx, http.MethodHead, key, nil, emptySHA256, nil)
	if err != nil {
		return ObjectInfo{}, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ObjectInfo{}, ErrObjectNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ObjectInfo{}, responseError("stat backup object", resp)
	}
	return objectInfo(resp), nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	req, err := s.request(ctx, http.MethodGet, key, nil, emptySHA256, nil)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ObjectInfo{}, ErrObjectNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError("download backup object", resp)
		resp.Body.Close()
		return nil, ObjectInfo{}, err
	}
	return resp.Body, objectInfo(resp), nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	req, err := s.request(ctx, http.MethodDelete, key, nil, emptySHA256, nil)
	if err != nil {
		return err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return responseError("delete incomplete backup object", resp)
}

func (s *S3Store) request(ctx context.Context, method, key string, body io.Reader, payloadSHA string, headers map[string]string) (*http.Request, error) {
	endpoint := strings.TrimSpace(s.Spec.Endpoint)
	if endpoint == "" {
		endpoint = "https://s3." + s.Spec.Region + ".amazonaws.com"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("backup store endpoint is invalid")
	}
	if parsed.Scheme == "http" && !s.Spec.AllowInsecure {
		return nil, errors.New("insecure backup store endpoint is not allowed")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + s.Spec.Bucket + "/" + strings.TrimPrefix(key, "/")
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	req.Header.Set("x-amz-date", now.Format("20060102T150405Z"))
	req.Header.Set("x-amz-content-sha256", payloadSHA)
	if s.Credential.SessionToken != "" {
		req.Header.Set("x-amz-security-token", s.Credential.SessionToken)
	}
	s.sign(req, payloadSHA, now)
	return req, nil
}

func (s *S3Store) sign(req *http.Request, payloadSHA string, now time.Time) {
	names := []string{"host"}
	values := map[string]string{"host": req.URL.Host}
	for name, entries := range req.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" {
			continue
		}
		names = append(names, lower)
		values[lower] = strings.Join(entries, ",")
	}
	sort.Strings(names)
	canonicalHeaders := strings.Builder{}
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.Join(strings.Fields(values[name]), " "))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")
	canonicalRequest := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.Query().Encode(), canonicalHeaders.String(), signedHeaders, payloadSHA}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	date := now.Format("20060102")
	scope := date + "/" + s.Spec.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + now.Format("20060102T150405Z") + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	dateKey := hmacSHA([]byte("AWS4"+s.Credential.SecretKey), date)
	regionKey := hmacSHA(dateKey, s.Spec.Region)
	serviceKey := hmacSHA(regionKey, "s3")
	signingKey := hmacSHA(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA(signingKey, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.Credential.AccessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hmacSHA(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}

func objectInfo(resp *http.Response) ObjectInfo {
	return ObjectInfo{Size: resp.ContentLength, SHA256: resp.Header.Get("x-amz-meta-sha256"), BackupID: resp.Header.Get("x-amz-meta-opsi-backup-id"), ETag: strings.Trim(resp.Header.Get("ETag"), "\""), VersionID: resp.Header.Get("x-amz-version-id")}
}

func responseError(operation string, resp *http.Response) error {
	message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s: status %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(message)))
}

func (s *S3Store) client() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return http.DefaultClient
}
