package scanner

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Client is the part of the S3 API the scanner uses: it reads and writes
// objects. It never lists and never deletes.
type S3Client interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

const resultsPrefix = "results/"

// getObject reads a whole object and fails when it is larger than maxBytes.
func (r *Runner) getObject(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	out, err := r.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.Opts.Bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("object %s is larger than %d bytes", key, maxBytes)
	}
	return data, nil
}

// putResult writes one object, and only under results/.
func (r *Runner) putResult(ctx context.Context, key string, body []byte) error {
	if !strings.HasPrefix(key, resultsPrefix) {
		return fmt.Errorf("refusing to write %s outside %s", key, resultsPrefix)
	}
	_, err := r.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(r.Opts.Bucket),
		Key:                  aws.String(key),
		Body:                 strings.NewReader(string(body)),
		ContentType:          aws.String("application/json"),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	})
	return err
}
