// Package kv is Harbormaster's one door to a Valkey or Redis server. It exposes only the commands both engines
// share (see AllowedCommands), so the same code runs against either. valkey.go is the only file that imports the
// client library; every other package depends on the Client interface.
package kv

import (
	"context"
	"strconv"
	"time"
)

// Value is one string reply. OK is false when the key or field does not exist.
type Value struct {
	S  string
	OK bool
}

// SetOptions are the SET modifiers Harbormaster uses. A zero TTL means no expiry.
type SetOptions struct {
	TTL time.Duration
	NX  bool
}

// Reply is the result of a script. A script that returns an integer or a string is read as a one-element slice.
type Reply interface {
	Ints() ([]int64, error)
	Strings() ([]string, error)
}

// Client is the set of operations Harbormaster needs from the store. Every method maps to commands in
// AllowedCommands. Keys and channel names are built with Key, which adds the install prefix.
type Client interface {
	Get(ctx context.Context, key string) (Value, error)
	MGet(ctx context.Context, keys ...string) ([]Value, error)
	// Set reports stored=false only when NX was given and the key already existed.
	Set(ctx context.Context, key, value string, o SetOptions) (stored bool, err error)
	Del(ctx context.Context, keys ...string) error
	PExpire(ctx context.Context, key string, ttl time.Duration) error
	ZRem(ctx context.Context, key string, members ...string) error
	ZRangeByScore(ctx context.Context, key string, min, max float64) ([]string, error)
	HMGet(ctx context.Context, key string, fields ...string) ([]Value, error)
	// Eval sends EVALSHA and falls back to EVAL once when the server does not know the script.
	Eval(ctx context.Context, s *Script, keys, args []string) (Reply, error)
	Publish(ctx context.Context, channel, payload string) error
	// Subscribe blocks until ctx ends. After a lost connection it reconnects and calls onResubscribe once the
	// subscription is back, so the caller can repair what it missed. Delivery is at most once.
	Subscribe(ctx context.Context, channel string, onMessage func(payload string), onResubscribe func()) error
	Ping(ctx context.Context) error
	ServerTime(ctx context.Context) (time.Time, error)
	// Key returns "<prefix>:v1:" followed by the parts joined with ":". It is also used for channel names.
	Key(parts ...string) string
	Close()
}

// Upper reports whether the store is currently usable. *Health satisfies it; fallbacks and handlers take it so
// they need not import the whole wrapper.
type Upper interface{ Up() bool }

// Millis formats a duration as whole milliseconds, the unit the Lua scripts take.
func Millis(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

// AllowedCommands is the command allow-list from the design, upper case. Every command that reaches the server,
// including the ones the client library sends while connecting, must be in it.
var AllowedCommands = map[string]bool{
	"GET": true, "MGET": true, "SET": true, "DEL": true, "PEXPIRE": true,
	// ZADD, ZCARD, ZREMRANGEBYSCORE and HSET are not client methods any more; the Lua scripts still run them server
	// side, where they show in the command statistics, so they stay listed.
	"ZADD": true, "ZREM": true, "ZCARD": true, "ZRANGEBYSCORE": true, "ZREMRANGEBYSCORE": true,
	"HSET": true, "HMGET": true,
	"EVALSHA": true, "EVAL": true, "SCRIPT LOAD": true,
	"PUBLISH": true, "SUBSCRIBE": true, "UNSUBSCRIBE": true,
	"PING": true, "TIME": true, "HELLO": true, "AUTH": true,
	"CLIENT SETNAME": true, "INFO SERVER": true,
}
