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
// The smallest valid id is 32 characters, so only the upper bound needs a check.
const maxIDLength = 2048

var (
	idAccount = regexp.MustCompile(`^[0-9]{12}$`)
	idName    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
	// idTaskARN has the region and the account as groups, for the check against fields 1 and 2.
	idTaskARN = regexp.MustCompile(`^arn:aws[a-z-]*:ecs:([a-z0-9-]{1,30}):([0-9]{12}):task/[A-Za-z0-9_-]+(/[0-9a-f]+)?$`)
)

// runIsRandom judges a long run. A run with no id piece (pieces split at "/") is judged by looksLikeRandomRun. A run
// with one is kept only when every other piece is plain; otherwise the whole run, ids included, is judged as it was
// before ids were known, so nothing but ids and plain pieces becomes visible.
func runIsRandom(run string) bool {
	if !strings.Contains(run, "/") {
		return !isHarbormasterID(run) && looksLikeRandomRun(run)
	}
	hasID := false
	for _, piece := range strings.Split(run, "/") {
		switch {
		case isHarbormasterID(piece):
			hasID = true
		case !isPlainPiece(piece):
			return looksLikeRandomRun(run)
		}
	}
	return !hasID && looksLikeRandomRun(run)
}

var (
	lowerHex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// idRegionPattern is the app's own (internal/api/notifications.go), so every region it accepts has working ids.
	idRegionPattern = regexp.MustCompile(`^[a-z0-9-]{1,30}$`)
)

const (
	plainPieceMax = 16 // a shorter piece holds too little to be a secret
	plainWordsMax = 64
)

// isPlainPiece reports whether a piece beside an id cannot hide a secret: empty, short, a UUID, an ECS task id (32
// lowercase hex), or plain words under 64 characters.
func isPlainPiece(piece string) bool {
	if len(piece) < plainPieceMax || uuid.MatchString(piece) || lowerHex32.MatchString(piece) {
		return true
	}
	if len(piece) >= plainWordsMax {
		return false
	}
	for _, word := range strings.FieldsFunc(piece, func(r rune) bool { return r == '-' || r == '_' }) {
		if !wordySegment.MatchString(word) {
			return false
		}
	}
	return true
}

// isHarbormasterID reports whether piece is a well-formed id as described above whose decoded fields hold no secret.
func isHarbormasterID(piece string) bool {
	if len(piece) > maxIDLength {
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
	if !idAccount.MatchString(account) || !idRegionPattern.MatchString(region) {
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
