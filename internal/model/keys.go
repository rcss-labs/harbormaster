package model

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Opaque IDs are base64url (no padding) of the key fields joined by "|".
// ECS ARNs never contain "|", so the join is unambiguous.
const idSeparator = "|"

type ClusterKey struct {
	AccountID string
	Region    string
	Cluster   string
}

type ServiceKey struct {
	ClusterKey
	Service string
}

func (k ClusterKey) ID() string {
	return encodeID(k.AccountID, k.Region, k.Cluster)
}

func (k ServiceKey) ID() string {
	return encodeID(k.AccountID, k.Region, k.Cluster, k.Service)
}

func ParseClusterID(id string) (ClusterKey, error) {
	fields, err := decodeID(id, 3)
	if err != nil {
		return ClusterKey{}, fmt.Errorf("cluster id: %w", err)
	}
	return ClusterKey{AccountID: fields[0], Region: fields[1], Cluster: fields[2]}, nil
}

func ParseServiceID(id string) (ServiceKey, error) {
	fields, err := decodeID(id, 4)
	if err != nil {
		return ServiceKey{}, fmt.Errorf("service id: %w", err)
	}
	return ServiceKey{
		ClusterKey: ClusterKey{AccountID: fields[0], Region: fields[1], Cluster: fields[2]},
		Service:    fields[3],
	}, nil
}

func TaskID(accountID, region, taskARN string) string {
	return encodeID(accountID, region, taskARN)
}

func ParseTaskID(id string) (accountID, region, taskARN string, err error) {
	fields, err := decodeID(id, 3)
	if err != nil {
		return "", "", "", fmt.Errorf("task id: %w", err)
	}
	return fields[0], fields[1], fields[2], nil
}

func encodeID(fields ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(fields, idSeparator)))
}

// decodeID returns exactly wantFields non-empty fields, or an error.
func decodeID(id string, wantFields int) ([]string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return nil, fmt.Errorf("not base64url: %w", err)
	}
	fields := strings.Split(string(raw), idSeparator)
	if len(fields) != wantFields {
		return nil, fmt.Errorf("want %d fields, got %d", wantFields, len(fields))
	}
	for index, field := range fields {
		if field == "" {
			return nil, fmt.Errorf("field %d is empty", index)
		}
	}
	return fields, nil
}
