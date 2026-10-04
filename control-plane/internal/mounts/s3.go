package mounts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// An S3-compatible mount (MinIO included), read with AWS Signature Version 4 and path-style addressing
// (<endpoint>/<bucket>/<key>), which every S3-compatible server accepts. Two calls only: ListObjectsV2 and GetObject.
// Source: AWS, "Signature Version 4 signing process" and "ListObjectsV2" (Amazon S3 API Reference).

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type s3Reader struct {
	endpoint, region, bucket, prefix string
	key, secret                      string
	client                           *http.Client
	now                              func() time.Time // tests
}

// key turns a path under the mount into the object key (the mount's prefix first).
func (r *s3Reader) objectKey(rel string) string {
	if r.prefix == "" {
		return rel
	}
	if rel == "" {
		return r.prefix + "/"
	}
	return r.prefix + "/" + rel
}

type listResult struct {
	Contents []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
		ETag string `xml:"ETag"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

func (r *s3Reader) Walk(ctx context.Context, prefix string, fn func(string, int64) error) error {
	return r.WalkStamped(ctx, prefix, func(rel string, size int64, _ string) error { return fn(rel, size) })
}

// WalkStamped implements StampedReader: an object's stamp is its ETag from the listing.
func (r *s3Reader) WalkStamped(ctx context.Context, prefix string, fn func(string, int64, string) error) error {
	if prefix != "" {
		if err := checkPath(prefix); err != nil {
			return err
		}
	}
	listPrefix := r.objectKey(prefix) // "<mount prefix>/" or ""
	if prefix != "" {
		listPrefix += "/"
	}
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "max-keys": {"1000"}}
		if listPrefix != "" {
			q.Set("prefix", listPrefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := r.do(ctx, "/"+r.bucket, q)
		if err != nil {
			return err
		}
		var res listResult
		err = xml.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&res)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("s3 list %s: %w", r.bucket, err)
		}
		for _, c := range res.Contents {
			if strings.HasSuffix(c.Key, "/") {
				continue // a "directory" placeholder
			}
			rel := c.Key
			if r.prefix != "" {
				rel = strings.TrimPrefix(c.Key, r.prefix+"/")
			}
			if err := fn(rel, c.Size, strings.Trim(c.ETag, `"`)); err != nil {
				if errors.Is(err, ErrStop) {
					return nil
				}
				return err
			}
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			return nil
		}
		token = res.NextContinuationToken
	}
}

func (r *s3Reader) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	if err := checkPath(rel); err != nil {
		return nil, err
	}
	resp, err := r.do(ctx, "/"+r.bucket+"/"+r.objectKey(rel), nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// do sends a signed GET and fails on a non-2xx answer (its body names the S3 error code).
func (r *s3Reader) do(ctx context.Context, p string, q url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+s3EscapePath(p), nil)
	if err != nil {
		return nil, err
	}
	if len(q) > 0 {
		req.URL.RawQuery = s3Query(q)
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	SignV4(req, r.key, r.secret, r.region, "s3", now().UTC())
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3 %s: %w", p, err)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("s3 %s: %s: %s", p, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// s3Escape is AWS's URI encoding: unreserved characters stay, everything else is %XX (upper case); slash stays
// when keepSlash (paths), not in query values.
func s3Escape(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func s3EscapePath(p string) string { return s3Escape(p, true) }

// s3Query is the canonical query string: keys sorted, keys and values encoded.
func s3Query(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, s3Escape(k, false)+"="+s3Escape(v, false))
		}
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// SignV4 signs a body-less request with AWS Signature Version 4 (headers host, x-amz-content-sha256, x-amz-date).
// The request's URL path and query must already be in canonical (s3Escape) form.
func SignV4(req *http.Request, accessKey, secretKey, region, service string, t time.Time) {
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
	host := req.URL.Host
	signed := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		"host:" + host + "\n" + "x-amz-content-sha256:" + emptySHA256 + "\n" + "x-amz-date:" + amzDate + "\n",
		signed,
		emptySHA256,
	}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+secretKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}
