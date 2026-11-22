package snapcache

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// maxDecoded bounds what Decode inflates, so a corrupt or hostile entry cannot exhaust memory.
const maxDecoded = 64 << 20

var errTooLarge = fmt.Errorf("snapshot model larger than %d bytes", maxDecoded)

// gzipWriters reuses compressors: a gzip.Writer carries a large internal state that is costly to allocate per model.
var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// Encode serialises a model as gzip-compressed JSON.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzipWriters.Get().(*gzip.Writer)
	defer gzipWriters.Put(zw)
	zw.Reset(&buf)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		return nil, fmt.Errorf("encode snapshot model: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("compress snapshot model: %w", err)
	}
	return buf.Bytes(), nil
}

// cappedReader fails with errTooLarge once more than maxDecoded bytes have been read.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errTooLarge
	}
	return n, err
}

// Decode reads what Encode wrote; anything else is an error.
func Decode(body []byte, v any) error {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("decompress snapshot model: %w", err)
	}
	defer zr.Close()
	capped := &cappedReader{r: zr, left: maxDecoded}
	dec := json.NewDecoder(capped)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, errTooLarge) {
			return errTooLarge
		}
		return fmt.Errorf("decode snapshot model: %w", err)
	}
	// Read to the end so the size cap and the gzip checksum cover the whole entry, as before.
	if _, err := io.Copy(io.Discard, capped); err != nil {
		if errors.Is(err, errTooLarge) {
			return errTooLarge
		}
		return fmt.Errorf("decompress snapshot model: %w", err)
	}
	return nil
}
