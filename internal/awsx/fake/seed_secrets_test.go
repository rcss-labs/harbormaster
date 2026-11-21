package fake

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// TestMockupEstate_SeedsSecretNames: config search demos need secret names, and only these two services have them.
func TestMockupEstate_SeedsSecretNames(t *testing.T) {
	e := MockupEstate(func() time.Time { return fixedNow })
	secretNames := map[string][]string{}
	for _, td := range e.TaskDefinitions {
		family := aws.ToString(td.Definition.Family)
		var names []string
		for _, c := range td.Definition.ContainerDefinitions {
			for _, s := range c.Secrets {
				names = append(names, aws.ToString(s.Name))
				wantPrefix := "arn:aws:secretsmanager:" + td.Region + ":" + td.AccountID + ":secret:"
				if !strings.HasPrefix(aws.ToString(s.ValueFrom), wantPrefix) {
					t.Errorf("%s secret %s valueFrom = %q, want prefix %q", family, aws.ToString(s.Name), aws.ToString(s.ValueFrom), wantPrefix)
				}
			}
		}
		if len(names) > 0 {
			sort.Strings(names)
			secretNames[family] = names
		}
	}
	want := map[string]string{"auth-service": "DATABASE_URL JWT_SIGNING_KEY", "api": "DATABASE_URL"}
	if len(secretNames) != len(want) {
		t.Fatalf("families with secrets = %v, want only %v", secretNames, want)
	}
	for family, names := range want {
		if got := strings.Join(secretNames[family], " "); got != names {
			t.Errorf("%s secrets = %q, want %q", family, got, names)
		}
	}
}
