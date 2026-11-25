package agentresult

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// MaxInstructionChars and MaxInstructionBytes bound the text an admin writes for a custom review.
const (
	MaxInstructionChars = 2000
	MaxInstructionBytes = 8000
)

// InstructionProblem names the first reason an instruction text is refused; InstructionsOK is the empty value.
type InstructionProblem string

const (
	InstructionsOK       InstructionProblem = ""
	InstructionsNotText  InstructionProblem = "not_text"
	InstructionsRequired InstructionProblem = "required"
	InstructionsTooLong  InstructionProblem = "too_long"
	InstructionsHidden   InstructionProblem = "hidden"
	InstructionsSecret   InstructionProblem = "secret"
)

var instructionLineEnds = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// CleanInstructions makes every line break a line feed and trims white space at both ends. Nothing else changes.
func CleanInstructions(raw string) string {
	return strings.TrimSpace(instructionLineEnds.Replace(raw))
}

// CheckInstructions returns the first problem of a cleaned text, or InstructionsOK. The byte cap comes first so no scan
// reads more than MaxInstructionBytes. It never returns or logs any part of the text.
func CheckInstructions(clean string) InstructionProblem {
	if len(clean) > MaxInstructionBytes {
		return InstructionsTooLong
	}
	if !utf8.ValidString(clean) || strings.IndexByte(clean, 0) >= 0 {
		return InstructionsNotText
	}
	if strings.TrimSpace(clean) == "" {
		return InstructionsRequired
	}
	if utf8.RuneCountInString(clean) > MaxInstructionChars {
		return InstructionsTooLong
	}
	for _, r := range clean {
		if isHiddenRune(r) {
			return InstructionsHidden
		}
	}
	if secretshape.ScrubText(clean) != clean {
		return InstructionsSecret
	}
	return InstructionsOK
}

// InstructionsHash is the lowercase hexadecimal SHA-256 of the cleaned text.
func InstructionsHash(clean string) string {
	sum := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(sum[:])
}

// EchoRun is the shortest copy Echoes reports, in normalised characters. It is longer than the summary rule's
// VerbatimRun because a request is short and its words are the review's subject: a summary may say a topic's own
// phrases, but not repeat the request.
const EchoRun = 80

// isTokenRune reports a character of a service or account name: a letter, a digit, a hyphen or an underscore.
func isTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_'
}

// normalizeTokens is Normalize that also returns, for each output character, the number of the name token (a maximal
// run of token characters in the raw text) it comes from, and -1 for a space between two tokens. Hyphens and
// underscores fold to spaces that still belong to their token.
func normalizeTokens(text string) (norm []rune, token []int32) {
	var cur int32 = -1  // token the previous raw character belonged to
	var next int32      // number of the next token
	gapInToken := false // every raw character since the last letter or digit was a hyphen or underscore, and there was one
	gap := false        // some raw character since the last letter or digit folded to a space
	inTok := false
	for _, r := range text {
		tok := isTokenRune(r)
		if tok && !inTok {
			cur = next
			next++
		}
		inTok = tok
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if gap && len(norm) > 0 {
				id := int32(-1)
				if gapInToken {
					id = cur
				}
				norm, token = append(norm, ' '), append(token, id)
			}
			gap, gapInToken = false, false
			norm, token = append(norm, unicode.ToLower(r)), append(token, cur)
			continue
		}
		if !gap {
			gapInToken = true
		}
		gap = true
		if r != '-' && r != '_' {
			gapInToken = false
		}
	}
	return norm, token
}

// insideOneToken reports whether norm[first..last] lies wholly inside one name token. A space at either edge is only a
// separator and is not counted, so a window that starts on the gap before a name and ends inside it is still the name.
func insideOneToken(norm []rune, token []int32, first, last int) bool {
	if norm[first] == ' ' && token[first] < 0 {
		first++
	}
	if norm[last] == ' ' && token[last] < 0 {
		last--
	}
	return token[first] >= 0 && token[first] == token[last]
}

// Echoes reports whether candidate holds EchoRun normalised characters in a row of source, with the case, spacing and
// punctuation Normalize folds, and one level of JSON escapes in the candidate decoded as a summary's check does. A
// window that lies wholly inside one name token (letters, digits, hyphens and underscores) is skipped, so a long
// service name that both texts hold is never an echo; a window that takes in the name and any text beside it counts.
// Time is linear in both texts. A source past the corpus cap cannot be checked, so it fails closed.
func Echoes(candidate, source string) bool {
	if len(source) > CorpusCap {
		return true
	}
	src, _ := normalizeTokens(source)
	if len(src) < EchoRun {
		return false
	}
	seen := make(map[uint64]int, len(src))
	var h, top uint64 = 0, 1
	for i := 1; i < EchoRun; i++ {
		top *= hashBase
	}
	for i, r := range src {
		if i >= EchoRun {
			h = (h-(uint64(src[i-EchoRun])+1)*top)*hashBase + uint64(r) + 1
		} else {
			h = h*hashBase + uint64(r) + 1
		}
		if i >= EchoRun-1 {
			if _, ok := seen[h]; !ok {
				seen[h] = i - EchoRun + 1
			}
		}
	}
	if echoesIn(candidate, src, seen) {
		return true
	}
	return strings.IndexByte(candidate, '\\') >= 0 && echoesIn(unescapeJSON(candidate), src, seen)
}

// echoesIn rolls over the candidate once against the hashed windows of the source, and checks a hit against the
// source's characters so a hash collision is never reported.
func echoesIn(candidate string, src []rune, seen map[uint64]int) bool {
	cand, token := normalizeTokens(candidate)
	var h, top uint64 = 0, 1
	for i := 1; i < EchoRun; i++ {
		top *= hashBase
	}
	for i, r := range cand {
		if i >= EchoRun {
			h = (h-(uint64(cand[i-EchoRun])+1)*top)*hashBase + uint64(r) + 1
		} else {
			h = h*hashBase + uint64(r) + 1
		}
		if i < EchoRun-1 {
			continue
		}
		at, ok := seen[h]
		first := i - EchoRun + 1
		if !ok || insideOneToken(cand, token, first, i) {
			continue
		}
		if slices.Equal(cand[first:i+1], src[at:at+EchoRun]) {
			return true
		}
	}
	return false
}
