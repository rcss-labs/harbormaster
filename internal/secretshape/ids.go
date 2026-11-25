package secretshape

import (
	"encoding/base64"
	"regexp"
	"strings"
)

// Harbormaster's own opaque ids (internal/model/keys.go) are base64url, without padding, of fields joined by "|":
//
//	cluster id  account|region|cluster
//	service id  account|region|cluster|service
//	task id     account|region|<ECS task ARN of that account and region>
//
// They look random, but the model needs them to name a service in its next call. The long-run rule exempts a run that
// is wholly an id; nothing else in this package treats them differently, so a name such as "password" or a value
// pattern still hides one, and an id inside a longer run (a path) is judged as any run is.
// maxIDLength bounds the decode. A run is at least 32 characters, so no lower bound is needed.
const maxIDLength = 2048

var (
	idAccount = regexp.MustCompile(`^[0-9]{12}$`)
	// A region such as us-east-1 or eusc-de-east-1; the length is capped apart.
	idRegion = regexp.MustCompile(`^[a-z]{2,4}(-[a-z]+)+-[0-9]{1,2}$`)
	idName   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
	// idTaskARN has the region and the account as groups, for the check against fields 1 and 2.
	idTaskARN = regexp.MustCompile(`^arn:aws[a-z-]*:ecs:([a-z0-9-]+):([0-9]{12}):task/[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)?$`)
)

// isHarbormasterID reports whether piece is a well-formed id as described above whose decoded fields hold no secret.
func isHarbormasterID(piece string) bool {
	if len(piece) > maxIDLength {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(piece)
	if err != nil {
		return false
	}
	for _, b := range raw {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	fields := strings.Split(string(raw), "|")
	if len(fields) != 3 && len(fields) != 4 {
		return false
	}
	account, region := fields[0], fields[1]
	if !idAccount.MatchString(account) || len(region) > 30 || !idRegion.MatchString(region) {
		return false
	}
	if len(fields) == 4 {
		if !idName.MatchString(fields[2]) || !idName.MatchString(fields[3]) {
			return false
		}
	} else if strings.HasPrefix(fields[2], "arn:") {
		arn := idTaskARN.FindStringSubmatch(fields[2])
		if arn == nil || arn[1] != region || arn[2] != account {
			return false
		}
	} else if !idName.MatchString(fields[2]) {
		return false
	}
	for _, field := range fields {
		if fieldHoldsSecret(field) {
			return false
		}
	}
	return true
}

// fieldHoldsSecret applies the value patterns and the URL password rule to one decoded field. The long-run rule is
// not applied: CDK and CloudFormation give services long random-looking names, and their ids must work.
func fieldHoldsSecret(field string) bool {
	return len(patternSpans(field)) > 0
}

// IDNames returns the names a well-formed Harbormaster id decodes to: the cluster and the service (empty for a cluster
// id). A task id gives the cluster only when its ARN carries one (task/<cluster>/<task>). ok is false for anything that
// is not such an id, with the same test the scrubber uses to let an id stand.
func IDNames(id string) (cluster, service string, ok bool) {
	// The base64 decoder skips \r and \n inside its input, so an id with a line break would decode to a real one.
	if strings.ContainsAny(id, "\r\n") || !isHarbormasterID(id) {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(id)
	if err != nil {
		return "", "", false
	}
	fields := strings.Split(string(raw), "|")
	switch {
	case len(fields) == 4:
		return fields[2], fields[3], true
	case strings.HasPrefix(fields[2], "arn:"):
		if arn := idTaskARN.FindStringSubmatch(fields[2]); arn != nil && arn[3] != "" {
			_, path, _ := strings.Cut(fields[2], ":task/")
			cluster, _, _ = strings.Cut(path, "/")
			return cluster, "", true
		}
		return "", "", true
	}
	return fields[2], "", true
}
