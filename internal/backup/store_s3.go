package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrObjectNotFound is returned by a Store when the requested key is absent.
var ErrObjectNotFound = errors.New("object not found")

// S3Config holds everything needed to reach one S3-compatible bucket. It carries
// resolved secrets and is assembled by the caller from a destination plus its
// referenced credential; it is never persisted.
type S3Config struct {
	Name         string
	Endpoint     string
	Region       string
	Bucket       string
	Prefix       string
	AccessKeyID  string
	SecretKey    string
	UsePathStyle bool
}

type S3Store struct {
	client *s3.Client
	cfg    S3Config
}

func NewS3Store(cfg S3Config) (*S3Store, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3 destination %q: bucket is required", cfg.Name)
	}
	if cfg.AccessKeyID == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("s3 destination %q: credentials are required", cfg.Name)
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	opts := s3.Options{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretKey, ""),
		UsePathStyle: cfg.UsePathStyle,
	}
	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
	}

	return &S3Store{client: s3.New(opts), cfg: cfg}, nil
}

func (s *S3Store) Name() string { return s.cfg.Name }

// EnsureBucket creates the store's bucket when it does not already exist. A
// freshly deployed object store starts empty, so a managed store needs its
// bucket made before the first backup can be mirrored to it.
func (s *S3Store) EnsureBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.cfg.Bucket)})
	if err == nil {
		return nil
	}

	_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.cfg.Bucket)})
	if err != nil {
		var owned *s3types.BucketAlreadyOwnedByYou
		var exists *s3types.BucketAlreadyExists
		if errors.As(err, &owned) || errors.As(err, &exists) {
			return nil
		}
		return fmt.Errorf("create bucket %q: %w", s.cfg.Bucket, err)
	}
	return nil
}

// fullKey prepends the destination prefix to a relative backup key.
func (s *S3Store) fullKey(key string) string {
	if s.cfg.Prefix == "" {
		return key
	}
	return path.Join(s.cfg.Prefix, key)
}

// relKey strips the destination prefix so returned keys match the Manager's
// relative <deployment>/<id>.tar.gz key space.
func (s *S3Store) relKey(full string) string {
	if s.cfg.Prefix == "" {
		return full
	}
	return strings.TrimPrefix(full, strings.TrimSuffix(s.cfg.Prefix, "/")+"/")
}

func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.cfg.Bucket),
		Key:           aws.String(s.fullKey(key)),
		Body:          r,
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	return out.Body, nil
}

func (s *S3Store) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
		Prefix: aws.String(s.fullKey(prefix)),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			key := s.relKey(aws.ToString(obj.Key))
			if !strings.HasSuffix(key, ".tar.gz") {
				continue
			}
			out = append(out, ObjectInfo{
				Key:     key,
				Size:    aws.ToInt64(obj.Size),
				ModTime: aws.ToTime(obj.LastModified),
			})
		}
	}
	return out, nil
}

// ListBuckets returns the names of every bucket reachable with the store's
// credentials. A store is configured with one bucket, but the server it points
// at (a self-hosted MinIO, say) can host many.
func (s *S3Store) ListBuckets(ctx context.Context) ([]string, error) {
	out, err := s.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("s3 list buckets: %w", err)
	}
	names := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	return names, nil
}

// WithBucket returns a view of the store scoped to another bucket at its root
// (no prefix), so the object browser can work across buckets on the same server.
func (s *S3Store) WithBucket(bucket string) *S3Store {
	cfg := s.cfg
	cfg.Bucket = bucket
	cfg.Prefix = ""
	return &S3Store{client: s.client, cfg: cfg}
}

// Bucket returns the bucket this store is scoped to.
func (s *S3Store) Bucket() string { return s.cfg.Bucket }

// ListObjectsPage returns one page of objects and a continuation token for the
// next page (empty when there are no more), so a browser can page through a
// bucket of any size instead of loading it whole.
func (s *S3Store) ListObjectsPage(ctx context.Context, prefix, token string, limit int32) ([]ObjectInfo, string, error) {
	in := &s3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
		Prefix: aws.String(s.fullKey(prefix)),
	}
	if limit > 0 {
		in.MaxKeys = aws.Int32(limit)
	}
	if token != "" {
		in.ContinuationToken = aws.String(token)
	}

	out, err := s.client.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("s3 list page: %w", err)
	}
	objects := make([]ObjectInfo, 0, len(out.Contents))
	for _, o := range out.Contents {
		objects = append(objects, ObjectInfo{
			Key:     s.relKey(aws.ToString(o.Key)),
			Size:    aws.ToInt64(o.Size),
			ModTime: aws.ToTime(o.LastModified),
		})
	}
	next := ""
	if aws.ToBool(out.IsTruncated) {
		next = aws.ToString(out.NextContinuationToken)
	}
	return objects, next, nil
}

// BucketStats returns the object count and total size of the store's bucket.
// Counting stops at limit (when > 0), reporting truncated=true, so the bucket
// list stays responsive on very large buckets.
func (s *S3Store) BucketStats(ctx context.Context, limit int) (count int, size int64, truncated bool, err error) {
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.cfg.Bucket)})
	for paginator.HasMorePages() {
		page, perr := paginator.NextPage(ctx)
		if perr != nil {
			return count, size, truncated, fmt.Errorf("s3 stats: %w", perr)
		}
		for _, o := range page.Contents {
			count++
			size += aws.ToInt64(o.Size)
			if limit > 0 && count >= limit {
				return count, size, true, nil
			}
		}
	}
	return count, size, false, nil
}

// DeleteBucket removes the store's bucket. S3 requires it to be empty.
func (s *S3Store) DeleteBucket(ctx context.Context) error {
	if _, err := s.client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(s.cfg.Bucket)}); err != nil {
		return fmt.Errorf("s3 delete bucket %q: %w", s.cfg.Bucket, err)
	}
	return nil
}

// ListObjects returns every object under prefix, unlike List which is scoped to
// backup archives (.tar.gz). It backs the object browser, where a store's full
// contents are shown.
func (s *S3Store) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
		Prefix: aws.String(s.fullKey(prefix)),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			out = append(out, ObjectInfo{
				Key:     s.relKey(aws.ToString(obj.Key)),
				Size:    aws.ToInt64(obj.Size),
				ModTime: aws.ToTime(obj.LastModified),
			})
		}
	}
	return out, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil && !isS3NotFound(err) {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return ObjectInfo{}, ErrObjectNotFound
		}
		return ObjectInfo{}, fmt.Errorf("s3 head %s: %w", key, err)
	}
	return ObjectInfo{
		Key:     key,
		Size:    aws.ToInt64(out.ContentLength),
		ModTime: aws.ToTime(out.LastModified),
	}, nil
}

func isS3NotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	var nf *s3types.NotFound
	return errors.As(err, &nsk) || errors.As(err, &nf)
}
