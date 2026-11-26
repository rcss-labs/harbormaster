package secretshape_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

func TestIDNames_ServiceClusterAndTaskIDs(t *testing.T) {
	long := strings.Repeat("payments-", 20) + "worker"
	for _, tc := range []struct {
		name, id, cluster, service string
	}{
		{"service", serviceID("prod-cluster", "checkout-api"), "prod-cluster", "checkout-api"},
		{"long service", serviceID("prod-cluster", long), "prod-cluster", long},
		{"cluster", clusterID("prod-cluster"), "prod-cluster", ""},
		{"task with cluster in the arn", taskID("arn:aws:ecs:" + idRegion + ":" + idAccount + ":task/prod-cluster/0123456789abcdef0123456789abcdef"), "prod-cluster", ""},
		{"task without cluster in the arn", taskID("arn:aws:ecs:" + idRegion + ":" + idAccount + ":task/0123456789abcdef0123456789abcdef"), "", ""},
	} {
		cluster, service, ok := secretshape.IDNames(tc.id)
		if !ok || cluster != tc.cluster || service != tc.service {
			t.Errorf("%s: got %q, %q, %v", tc.name, cluster, service, ok)
		}
	}
}

func TestIDNames_RefusesInvalidPaddedAndNonCanonicalIDs(t *testing.T) {
	for name, id := range refusedIDs(t, "checkout-api") {
		if cluster, service, ok := secretshape.IDNames(id); ok || cluster != "" || service != "" {
			t.Errorf("%s: got %q, %q, %v", name, cluster, service, ok)
		}
	}
}

// refusedIDs are texts that are no ids, most of them made from an id of service.
func refusedIDs(t *testing.T, service string) map[string]string {
	t.Helper()
	good := serviceID("prod-cluster", service)
	enc := base64.RawURLEncoding.EncodeToString
	spare := idAccount + "|" + idRegion + "|prod-cluster|" + service
	if len(spare)%3 == 0 {
		spare += "b"
	}
	return map[string]string{
		"empty":            "",
		"line feed":        good[:10] + "\n" + good[10:],
		"carriage return":  good[:10] + "\r" + good[10:],
		"trailing newline": good + "\n",
		"prose":            "the checkout service",
		"padded":           good + "=",
		"standard base64":  strings.NewReplacer("-", "+", "_", "/").Replace(enc([]byte(idAccount + "|" + idRegion + "|prod-cluster|" + service + "~~"))),
		"non canonical":    nonCanonical(t, spare),
		"bad account":      enc([]byte("12345|" + idRegion + "|prod-cluster|" + service)),
		"bad region":       enc([]byte(idAccount + "|nowhere|prod-cluster|" + service)),
		"five fields":      enc([]byte(idAccount + "|" + idRegion + "|a|b|" + service)),
		"two fields":       enc([]byte(idAccount + "|" + idRegion)),
		"space in name":    enc([]byte(idAccount + "|" + idRegion + "|prod cluster|" + service)),
		"control byte":     enc([]byte(idAccount + "|" + idRegion + "|prod\x01|" + service)),
		"task other acct":  taskID("arn:aws:ecs:" + idRegion + ":999999999999:task/" + service + "/abc"),
		"secret in a name": serviceID(service, idAWSKey),
		"over the bound":   enc([]byte(idAccount + "|" + idRegion + "|" + strings.Repeat("c", 3000) + "|" + service)),
	}
}

// nonCanonical encodes raw so that the unused trailing bits of the last character are not zero.
func nonCanonical(t *testing.T, raw string) string {
	t.Helper()
	good := base64.RawURLEncoding.EncodeToString([]byte(raw))
	if len(raw)%3 == 0 {
		t.Fatal("raw has no spare bits")
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, good[len(good)-1])
	return good[:len(good)-1] + string(alphabet[last+1])
}
