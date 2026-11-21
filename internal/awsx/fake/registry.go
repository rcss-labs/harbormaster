package fake

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/rcss-labs/harbormaster/internal/registry"
)

// Registry answers public registry reads from an in-memory table, for --fake-aws and tests. It records the host of every
// request and never opens a socket.
type Registry struct {
	mu     sync.Mutex
	hosts  []string
	images map[string]registryImage
}

type registryImage struct {
	body      []byte
	mediaType string
}

// NewRegistry holds the public images the seeded estate uses: the otel collector sidecar of payments-api, a
// multi-architecture image with an arm64 build.
func NewRegistry() *Registry {
	r := &Registry{images: map[string]registryImage{}}
	r.add("public.ecr.aws", "aws-observability/aws-otel-collector", "v0.40.0", true)
	return r
}

func (r *Registry) add(host, name, tag string, arm64 bool) {
	e := &Estate{ImageIndexes: map[string]ImageIndexSpec{host + "/" + name: {ARM64: arm64}}}
	body, mediaType := e.manifestFor(host+"/"+name, tag)
	r.images[host+"/"+name+":"+tag] = registryImage{body: body, mediaType: mediaType}
}

// Hosts lists the host of every request so far, in order.
func (r *Registry) Hosts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.hosts...)
}

func (r *Registry) lookup(ref registry.Ref) (registryImage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, ref.Host)
	image, ok := r.images[ref.Host+"/"+ref.Name+":"+ref.Reference]
	if !ok {
		return registryImage{}, registry.ErrNotFound
	}
	return image, nil
}

// Digest is the digest of the manifest body, as a registry reports it.
func (r *Registry) Digest(_ context.Context, ref registry.Ref) (string, error) {
	image, err := r.lookup(ref)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(image.body)), nil
}

// Manifest is the stored manifest body and its media type.
func (r *Registry) Manifest(_ context.Context, ref registry.Ref) ([]byte, string, error) {
	image, err := r.lookup(ref)
	if err != nil {
		return nil, "", err
	}
	return image.body, image.mediaType, nil
}
