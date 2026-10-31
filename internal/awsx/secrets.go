package awsx

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// SecretReader reads a Secrets Manager secret string.
type SecretReader interface {
	ReadSecret(ctx context.Context, arn string) (string, error)
}

type secretsManagerReader struct{ client *secretsmanager.Client }

// NewSecretReader reads secrets with the base (Harbormaster task role) config.
func NewSecretReader(base aws.Config) SecretReader {
	return secretsManagerReader{client: secretsmanager.NewFromConfig(base)}
}

func (s secretsManagerReader) ReadSecret(ctx context.Context, arn string) (string, error) {
	out, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(arn)})
	if err != nil {
		return "", fmt.Errorf("get secret value: %w", err)
	}
	if out.SecretString == nil {
		return "", errors.New("secret has no string value")
	}
	return *out.SecretString, nil
}
