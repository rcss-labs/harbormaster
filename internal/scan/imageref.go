package scan

import (
	"regexp"
	"strings"
)

var (
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

var ecrHostPattern = regexp.MustCompile(`^([0-9]{12})\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com(\.cn)?$`)

// Ref is a parsed container image reference.
type Ref struct{ Kind, Host, AccountID, Region, Repository, Tag, Digest string }

// ParseImageRef splits an image string into registry host, repository, tag and
// digest, and classifies the registry. A reference with no registry host is
// Docker Hub, and a single-segment Docker Hub name lives under library/.
func ParseImageRef(image string) Ref {
	var r Ref
	rest := image
	validParts := true
	if i := strings.Index(rest, "@"); i >= 0 {
		if digestPattern.MatchString(rest[i+1:]) {
			r.Digest = rest[i+1:]
		} else {
			validParts = false
		}
		rest = rest[:i]
	}

	host, path := "", rest
	if i := strings.Index(rest, "/"); i >= 0 {
		first := rest[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host, path = first, rest[i+1:]
		}
	}
	if host == "" {
		host = "docker.io"
		if !strings.Contains(path, "/") {
			// the tag separator is still inside path here, so prefix before splitting it
			path = "library/" + path
		}
	}

	// a colon in the last path segment is the tag separator; earlier ones belong to a port
	if i := strings.LastIndex(path, ":"); i >= 0 && !strings.Contains(path[i:], "/") {
		r.Tag = path[i+1:]
		path = path[:i]
	}
	r.Host, r.Repository = host, path

	// an unparseable repository, tag or digest is never reported as ECR
	if !repositoryPattern.MatchString(path) || (r.Tag != "" && !tagPattern.MatchString(r.Tag)) {
		validParts = false
	}
	if !validParts {
		r.Kind = KindOther
		return r
	}

	switch m := ecrHostPattern.FindStringSubmatch(host); {
	case m != nil:
		r.Kind, r.AccountID, r.Region = KindECR, m[1], m[2]
	case host == "public.ecr.aws":
		r.Kind = KindECRPublic
	default:
		r.Kind = KindOther
	}
	return r
}

// ImageRef is host/repository with no tag or digest.
func (r Ref) ImageRef() string { return r.Host + "/" + r.Repository }
