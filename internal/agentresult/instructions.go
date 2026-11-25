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

// Echoes reports whether candidate holds VerbatimRun normalised characters in a row of source, by the rule a summary's
// copy check follows. A source past the corpus cap cannot be checked, so it fails closed.
func Echoes(candidate, source string) bool {
	corpus := &Corpus{}
	corpus.Add(source)
	if corpus.Full() {
		return true
	}
	return corpus.HasRunIn(candidate)
}
