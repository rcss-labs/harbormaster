package kv

import (
	"crypto/sha1" //nolint:gosec // the server identifies scripts by SHA-1; this is not a security use
	"encoding/hex"
)

// Script is a Lua script and its SHA-1, built once by NewScript. Scripts use only redis.call and read time from
// TIME, never from the caller.
type Script struct {
	source string
	sha    string
}

// NewScript prepares a script for Client.Eval.
func NewScript(src string) *Script {
	sum := sha1.Sum([]byte(src)) //nolint:gosec // see import
	return &Script{source: src, sha: hex.EncodeToString(sum[:])}
}
