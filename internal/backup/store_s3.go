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
