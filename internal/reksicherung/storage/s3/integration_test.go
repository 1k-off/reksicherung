//go:build integration

package s3

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/1k-off/reksicherung/internal/reksicherung/storage"
)

func TestLocalS3CompatibleService(t *testing.T) {
	endpoint := os.Getenv("REKSICHERUNG_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("set REKSICHERUNG_MINIO_ENDPOINT to run the local S3 integration test")
	}
	parsed, err := validateEndpoint(endpoint)
	if err != nil || !isLoopbackHost(parsed.Hostname()) {
		t.Fatal("integration endpoint must be a valid loopback URL")
	}

	bucket := fmt.Sprintf("reksicherung-integration-%d", time.Now().UnixNano())
	client, err := New(Config{
		Endpoint:        endpoint,
		Region:          "us-east-1",
		Bucket:          bucket,
		AccessKeyID:     "dummy-integration-access-key",
		SecretAccessKey: "dummy-integration-secret-key",
		MaxObjectSize:   1 << 20,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = client.api.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatalf("create test bucket: %v", err)
	}
	defer func() {
		_, _ = client.api.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	}()

	const key = "integration/backup.bak"
	const contents = "local object-store integration fixture"
	_, err = client.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(contents),
	})
	if err != nil {
		t.Fatalf("put test object: %v", err)
	}
	defer func() {
		_, _ = client.api.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
	}()

	object, err := client.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if object.Key != key || object.Size != int64(len(contents)) {
		t.Fatalf("Stat() = %#v", object)
	}

	page, err := client.List(ctx, storage.ListOptions{Prefix: "integration/", Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != key {
		t.Fatalf("List() = %#v", page)
	}

	reader, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(data) != contents {
		t.Fatalf("Get() = (%q, read error %v, close error %v)", data, readErr, closeErr)
	}
}
