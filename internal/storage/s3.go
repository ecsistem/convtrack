package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Client struct {
	client    *s3.Client
	presigner *s3.PresignClient
	bucket    string
}

func NewS3Client(ctx context.Context) (*S3Client, error) {
	bucket := getEnv("S3_DEFAULT_BUCKET", getEnv("S3_BUCKET", "convtrack-replays"))

	accessKey := getEnv("S3_ACCESS_KEY_ID", os.Getenv("S3_ACCESS_KEY"))
	secretKey  := getEnv("S3_SECRET_ACCESS_KEY", os.Getenv("S3_SECRET_KEY"))
	region     := getEnv("S3_REGION", "us-east-1")

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}

	// Se tiver credenciais explícitas (staging / S3-compatível), usa elas
	if accessKey != "" && secretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load config: %w", err)
	}

	var clientOpts []func(*s3.Options)

	// Endpoint customizado — útil para LocalStack em dev
	if endpoint := os.Getenv("S3_ENDPOINT"); endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // necessário para LocalStack e MinIO
		})
	}

	client := s3.NewFromConfig(cfg, clientOpts...)
	presigner := s3.NewPresignClient(client)

	return &S3Client{
		client:    client,
		presigner: presigner,
		bucket:    bucket,
	}, nil
}

// UploadReplay comprime os dados em gzip e faz upload para S3.
// Retorna a chave do objeto (key) no bucket.
func (c *S3Client) UploadReplay(ctx context.Context, key string, data []byte) error {
	compressed, err := gzipCompress(data)
	if err != nil {
		return fmt.Errorf("s3: compress: %w", err)
	}

	_, err = c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:          aws.String(c.bucket),
		Key:             aws.String(key),
		Body:            bytes.NewReader(compressed),
		ContentType:     aws.String("application/json"),
		ContentEncoding: aws.String("gzip"),
		// Objetos de replay ficam privados — acessados via presigned URL
		ACL: types.ObjectCannedACLPrivate,
	})
	if err != nil {
		return fmt.Errorf("s3: put object %q: %w", key, err)
	}
	return nil
}

// PresignedURL gera uma URL temporária para o frontend reproduzir o replay.
// TTL padrão: 1 hora.
func (c *S3Client) PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if ttl == 0 {
		ttl = time.Hour
	}
	req, err := c.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3: presign %q: %w", key, err)
	}
	return req.URL, nil
}

// DeleteReplay remove um objeto do bucket (GDPR / limpeza)
func (c *S3Client) DeleteReplay(ctx context.Context, key string) error {
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	return err
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
