package main

import (
	"context"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Store interface {
	Put(ctx context.Context, key string, body io.Reader) error
	List(ctx context.Context, prefix string) ([]Object, error)
	Delete(ctx context.Context, key string) error
}

type Object struct {
	Key          string
	LastModified time.Time
}

// prune deletes all but the newest b.Keep dumps of b. Keys sort by upload
// time, so the newest dumps sort last.
func prune(ctx context.Context, store Store, b *Backup) error {
	objects, err := store.List(ctx, b.Name+"/")
	if err != nil {
		return err
	}
	slices.SortFunc(objects, func(x, y Object) int { return strings.Compare(y.Key, x.Key) })

	for _, o := range objects[min(b.Keep, len(objects)):] {
		if err := store.Delete(ctx, o.Key); err != nil {
			return err
		}
		log.Printf("%s: pruned %s", b.Name, o.Key)
	}
	return nil
}

// newestDump returns when the most recent dump of name was stored, or the
// zero time if there are none.
func newestDump(ctx context.Context, store Store, name string) (time.Time, error) {
	objects, err := store.List(ctx, name+"/")
	if err != nil {
		return time.Time{}, err
	}
	var newest time.Time
	for _, o := range objects {
		if o.LastModified.After(newest) {
			newest = o.LastModified
		}
	}
	return newest, nil
}

type s3Store struct {
	client   *s3.Client
	uploader *transfermanager.Client
	bucket   string
}

func newS3Store(ctx context.Context, cfg S3Config) (*s3Store, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
		// Not every S3-compatible service accepts the checksums newer SDK
		// versions add to every request, so only send them when required.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})

	uploader := transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = 16 << 20
		o.Concurrency = 2
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		// Gives the multipart abort its own context, so it still runs after a
		// client disconnect has cancelled the upload's context.
		o.FailTimeout = time.Minute
	})

	return &s3Store{client: client, uploader: uploader, bucket: cfg.Bucket}, nil
}

func (s *s3Store) Put(ctx context.Context, key string, body io.Reader) error {
	_, err := s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String("application/zstd"),
	})
	return err
}

func (s *s3Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var objects []Object
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			objects = append(objects, Object{Key: aws.ToString(o.Key), LastModified: aws.ToTime(o.LastModified)})
		}
	}
	return objects, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}
