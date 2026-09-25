package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type R2Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
}

type ObjectInfo struct {
	Size        int64
	ContentType string
}

type ObjectStore interface {
	PresignPut(context.Context, string, string, int64, time.Duration) (string, http.Header, error)
	PresignGet(context.Context, string, time.Duration) (string, error)
	Head(context.Context, string) (ObjectInfo, error)
	Delete(context.Context, string) error
	Get(context.Context, string) (io.ReadCloser, error)
	Put(context.Context, string, string, io.Reader, int64) error
}

type R2Store struct {
	bucket  string
	client  *s3.Client
	presign *s3.PresignClient
}

func NewR2Store(cfg R2Config) (*R2Store, error) {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("R2 endpoint, bucket, access key and secret key are required")
	}
	client := s3.New(s3.Options{
		Region:       "auto",
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		BaseEndpoint: aws.String(strings.TrimSuffix(cfg.Endpoint, "/")),
		UsePathStyle: true,
	})
	return &R2Store{bucket: cfg.Bucket, client: client, presign: s3.NewPresignClient(client)}, nil
}

func (s *R2Store) PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, http.Header, error) {
	request, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String(contentType), ContentLength: aws.Int64(size),
	}, func(options *s3.PresignOptions) { options.Expires = ttl })
	if err != nil {
		return "", nil, fmt.Errorf("presign R2 upload: %w", err)
	}
	return request.URL, request.SignedHeader, nil
}

func (s *R2Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	request, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}, func(options *s3.PresignOptions) { options.Expires = ttl })
	if err != nil {
		return "", fmt.Errorf("presign R2 download: %w", err)
	}
	return request.URL, nil
}

func (s *R2Store) Head(ctx context.Context, key string) (ObjectInfo, error) {
	response, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("head R2 object: %w", err)
	}
	return ObjectInfo{Size: aws.ToInt64(response.ContentLength), ContentType: aws.ToString(response.ContentType)}, nil
}

func (s *R2Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("delete R2 object: %w", err)
	}
	return nil
}

func (s *R2Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	response, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("get R2 object: %w", err)
	}
	return response.Body, nil
}

func (s *R2Store) Put(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String(contentType), ContentLength: aws.Int64(size), Body: body,
	})
	if err != nil {
		return fmt.Errorf("put R2 object: %w", err)
	}
	return nil
}
