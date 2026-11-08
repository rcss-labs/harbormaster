package scan

import "testing"

func TestParseImageRef(t *testing.T) {
	dg := "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ecr := "482109375512.dkr.ecr.us-east-1.amazonaws.com"
	tests := []struct {
		name  string
		image string
		want  Ref
		ref   string
	}{
		{"ecr tag", ecr + "/payments-api:1.18.2",
			Ref{Kind: KindECR, Host: ecr, AccountID: "482109375512", Region: "us-east-1", Repository: "payments-api", Tag: "1.18.2"},
			ecr + "/payments-api"},
		{"ecr digest", ecr + "/team/payments-api@" + dg,
			Ref{Kind: KindECR, Host: ecr, AccountID: "482109375512", Region: "us-east-1", Repository: "team/payments-api", Digest: dg},
			ecr + "/team/payments-api"},
		{"ecr tag and digest", ecr + "/payments-api:1.18.2@" + dg,
			Ref{Kind: KindECR, Host: ecr, AccountID: "482109375512", Region: "us-east-1", Repository: "payments-api", Tag: "1.18.2", Digest: dg},
			ecr + "/payments-api"},
		{"ecr china", "482109375512.dkr.ecr.cn-north-1.amazonaws.com.cn/app:1",
			Ref{Kind: KindECR, Host: "482109375512.dkr.ecr.cn-north-1.amazonaws.com.cn", AccountID: "482109375512", Region: "cn-north-1", Repository: "app", Tag: "1"},
			"482109375512.dkr.ecr.cn-north-1.amazonaws.com.cn/app"},
		{"ecr public", "public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0",
			Ref{Kind: KindECRPublic, Host: "public.ecr.aws", Repository: "aws-observability/aws-otel-collector", Tag: "v0.40.0"},
			"public.ecr.aws/aws-observability/aws-otel-collector"},
		{"docker hub official", "nginx:1.27",
			Ref{Kind: KindOther, Host: "docker.io", Repository: "library/nginx", Tag: "1.27"},
			"docker.io/library/nginx"},
		{"ghcr no tag", "ghcr.io/org/app",
			Ref{Kind: KindOther, Host: "ghcr.io", Repository: "org/app"},
			"ghcr.io/org/app"},
		{"registry with port", "localhost:5000/app:2",
			Ref{Kind: KindOther, Host: "localhost:5000", Repository: "app", Tag: "2"},
			"localhost:5000/app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseImageRef(tt.image)
			if got != tt.want {
				t.Errorf("ParseImageRef(%q) = %+v, want %+v", tt.image, got, tt.want)
			}
			if got.ImageRef() != tt.ref {
				t.Errorf("ImageRef() = %q, want %q", got.ImageRef(), tt.ref)
			}
		})
	}
}

func TestParseImageRef_HostileAndLookAlikeInputsAreNotECR(t *testing.T) {
	ecr := "482109375512.dkr.ecr.us-east-1.amazonaws.com"
	hostile := []string{
		ecr + "/app@../../x",
		ecr + "/app@sha256:short",
		ecr + "/../../../etc:1\nFAKE",
		ecr + "/app:bad tag",
		ecr + "/App:1",
		ecr + "/",
		ecr + "//app:1",
		ecr + ".evil.example/app:1",
		ecr + "@evil.example/app",
		"evil.example/" + ecr + "/app",
		"public.ecr.aws/../x:1",
	}
	for _, image := range hostile {
		got := ParseImageRef(image)
		if got.Kind != KindOther {
			t.Errorf("ParseImageRef(%q).Kind = %q, want other", image, got.Kind)
		}
		if got.Digest != "" && !digestPattern.MatchString(got.Digest) {
			t.Errorf("ParseImageRef(%q) kept invalid digest %q", image, got.Digest)
		}
	}
}
