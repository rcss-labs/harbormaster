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
// They look random, but the model needs them to name a service in its next call. The long-run rule sets such a piece
// aside; nothing else in this package treats them differently, so a name such as "password" or a value pattern still
// hides one.
const (
	minIDLength = 16
	maxIDLength = 2048
)

var (
	idAccount = regexp.MustCompile(`^[0-9]{12}$`)
	idRegion  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	idName    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
	// idTaskARN has the region and the account as groups, for the check against fields 1 and 2.
	idTaskARN = regexp.MustCompile(`^arn:aws[a-z-]*:ecs:([a-z0-9-]+):([0-9]{12}):task/[A-Za-z0-9_-]+(/[0-9a-f]+)?$`)
)

// runIsRandom judges a long run: pieces between slashes that are Harbormaster ids are set aside, and what is left,
// rejoined with slashes, is judged as any run is. A run with no id is judged as it is.
func runIsRandom(run string) bool {
	if !strings.Contains(run, "/") {
		if isHarbormasterID(run) {
			return false
		}
		return looksLikeRandomRun(run)
	}
	pieces := strings.Split(run, "/")
	kept := pieces[:0]
	for _, piece := range pieces {
		if !isHarbormasterID(piece) {
			kept = append(kept, piece)
		}
	}
	if len(kept) == len(pieces) {
		return looksLikeRandomRun(run)
	}
	rest := strings.Join(kept, "/")
	return len(rest) >= 32 && looksLikeRandomRun(rest)
}

// isHarbormasterID reports whether piece is a well-formed id as described above whose decoded fields hold no secret.
func isHarbormasterID(piece string) bool {
	if len(piece) < minIDLength || len(piece) > maxIDLength {
		return false
	}
	for i := 0; i < len(piece); i++ {
		c := piece[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false // padding, "+", "/" and anything the decoder would skip or reject
		}
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
	if !idAccount.MatchString(account) || !idRegion.MatchString(region) {
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

// fieldHoldsSecret applies the value rules to one decoded field. It does not apply the id rule again.
func fieldHoldsSecret(field string) bool {
	if len(patternSpans(field)) > 0 {
		return true
	}
	for _, m := range longRun.FindAllString(field, -1) {
		if looksLikeRandomRun(m) {
			return true
		}
	}
	return false
}
