package agentresult

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

// Echoes reports whether candidate holds EchoRun normalised characters in a row of source, by the corpus rule a summary
// follows (Normalize, one level of JSON escapes decoded on either side) with the run lengthened. A window wholly inside
// one name the source holds, a token of letters, digits, hyphens and underscores of at most 128 characters, is not
// counted, so a long service name is never an echo; a window that takes in a name and text beside it is. A source past
// the corpus cap cannot be checked, so it fails closed.
func Echoes(candidate, source string) bool {
	if len(source) > CorpusCap {
		return true
	}
	c := &Corpus{run: EchoRun}
	c.store(source)
	c.AllowNames(nameTokens(source)...)
	return c.hasRunAllowing(candidate)
}

// nameTokens returns the maximal runs of ASCII letters, digits, hyphens and underscores of text. AllowNames drops the
// ones that are too long.
func nameTokens(text string) []string {
	var tokens []string
	start := -1
	for i := 0; i <= len(text); i++ {
		if i < len(text) && isNameByte(text[i]) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			tokens = append(tokens, text[start:i])
			start = -1
		}
	}
	return tokens
}
