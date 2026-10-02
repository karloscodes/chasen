package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// s3Config points to a bucket on any S3-compatible store (S3, R2, B2, Hetzner).
// Requests use path-style URLs: <endpoint>/<bucket>/<key>.
type s3Config struct {
	Endpoint        string `yaml:"endpoint"`
	Region          string `yaml:"region"`
	Bucket          string `yaml:"bucket"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
}

var s3Client = &http.Client{Timeout: 30 * time.Minute}

// do sends one request signed with AWS Signature Version 4.
// ponytail: one PUT per object, so 5 GB per database. Add multipart upload when a database gets near that.
func (c *s3Config) do(method, key string, query url.Values, body io.Reader, size int64) (*http.Response, error) {
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil || endpoint.Host == "" {
		return nil, fmt.Errorf("invalid backup.s3.endpoint %q", c.Endpoint)
	}
	path := "/" + c.Bucket
	if key != "" {
		path += "/" + key
	}
	// AWS wants spaces as %20. url.Values.Encode writes them as "+".
	rawQuery := strings.ReplaceAll(query.Encode(), "+", "%20")

	req, err := http.NewRequest(method, endpoint.Scheme+"://"+endpoint.Host, body)
	if err != nil {
		return nil, err
	}
	req.URL.Path, req.URL.RawPath, req.URL.RawQuery = path, s3Escape(path), rawQuery
	req.ContentLength = size

	now := time.Now().UTC()
	date, day := now.Format("20060102T150405Z"), now.Format("20060102")
	req.Header.Set("x-amz-date", date)
	req.Header.Set("x-amz-content-sha256", "UNSIGNED-PAYLOAD")

	canonical := strings.Join([]string{
		method,
		s3Escape(path),
		rawQuery,
		"host:" + endpoint.Host + "\nx-amz-content-sha256:UNSIGNED-PAYLOAD\nx-amz-date:" + date + "\n",
		"host;x-amz-content-sha256;x-amz-date",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	scope := day + "/" + c.Region + "/s3/aws4_request"
	hash := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + date + "\n" + scope + "\n" + hex.EncodeToString(hash[:])

	signingKey := []byte("AWS4" + c.SecretAccessKey)
	for _, part := range []string{day, c.Region, "s3", "aws4_request"} {
		signingKey = hmacSHA256(signingKey, part)
	}
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=%s",
		c.AccessKeyID, scope, hex.EncodeToString(hmacSHA256(signingKey, toSign))))

	resp, err := s3Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("s3 %s %s: %s: %s", method, path, resp.Status, msg)
	}
	return resp, nil
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// s3Escape percent-encodes a path the way Signature Version 4 requires.
func s3Escape(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '/':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// createBucket makes the bucket. A bucket that exists already is fine. Some
// stores say "you own it already", and others, like Hetzner for a bucket
// made in its console, only say "the name is taken". So this does not decide
// whose bucket it is: the caller writes a test object, and that proves it.
func (c *s3Config) createBucket() error {
	resp, err := c.do("PUT", "", nil, nil, 0)
	if err != nil {
		if strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") || strings.Contains(err.Error(), "BucketAlreadyExists") {
			return nil
		}
		return err
	}
	return resp.Body.Close()
}

func (c *s3Config) putFile(key, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	resp, err := c.do("PUT", key, nil, f, info.Size())
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *s3Config) getFile(key, file string) error {
	resp, err := c.do("GET", key, nil, nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func (c *s3Config) delete(key string) error {
	resp, err := c.do("DELETE", key, nil, nil, 0)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// list returns every object key that starts with prefix.
func (c *s3Config) list(prefix string) ([]string, error) {
	objects, err := c.objects(prefix)
	keys := make([]string, len(objects))
	for i, object := range objects {
		keys[i] = object.Key
	}
	return keys, err
}

// s3Object is one object of the bucket.
type s3Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// objects returns the objects of the bucket whose key starts with the prefix.
func (c *s3Config) objects(prefix string) ([]s3Object, error) {
	var objects []s3Object
	query := url.Values{"list-type": {"2"}, "prefix": {prefix}}
	for {
		resp, err := c.do("GET", "", query, nil, 0)
		if err != nil {
			return nil, err
		}
		var page struct {
			Contents              []s3Object
			IsTruncated           bool
			NextContinuationToken string
		}
		err = xml.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		objects = append(objects, page.Contents...)
		if !page.IsTruncated {
			return objects, nil
		}
		query.Set("continuation-token", page.NextContinuationToken)
	}
}
