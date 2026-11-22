package snapcache

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
)

// maxDecoded bounds what Decode inflates, so a corrupt or hostile entry cannot exhaust memory.
const maxDecoded = 64 << 20

// Encode serialises a model as gzip-compressed JSON.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		return nil, fmt.Errorf("encode snapshot model: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("compress snapshot model: %w", err)
	}
	return buf.Bytes(), nil
}

// Decode reads what Encode wrote; anything else is an error.
func Decode(body []byte, v any) error {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("decompress snapshot model: %w", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, maxDecoded+1))
	if err != nil {
		return fmt.Errorf("decompress snapshot model: %w", err)
	}
	if len(raw) > maxDecoded {
		return fmt.Errorf("snapshot model larger than %d bytes", maxDecoded)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode snapshot model: %w", err)
	}
	return nil
}
