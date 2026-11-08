package awsx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var testHomeConfig = HomeConfig{
	ClusterARN:        "arn:aws:ecs:us-east-1:111111111111:cluster/harbormaster",
	TaskDefinitionARN: "arn:aws:ecs:us-east-1:111111111111:task-definition/harbormaster-scanner:3",
	SecurityGroup:     "sg-0123456789abcdef0",
	Bucket:            "harbormaster-scan-111111111111",
	Subnets:           []string{"subnet-aaa", "subnet-bbb"},
}

type recordingECS struct {
	runInput *ecs.RunTaskInput
}

func (r *recordingECS) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	r.runInput = in
	return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: aws.String("arn:aws:ecs:us-east-1:111111111111:task/harbormaster/abc")}}}, nil
}

func (r *recordingECS) DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	return &ecs.DescribeTasksOutput{}, nil
}

func (r *recordingECS) StopTask(context.Context, *ecs.StopTaskInput, ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	return &ecs.StopTaskOutput{}, nil
}

func (r *recordingECS) called(t *testing.T) *ecs.RunTaskInput {
	t.Helper()
	if r.runInput == nil {
		t.Fatal("ECS RunTask was never called")
	}
	return r.runInput
}

func environmentOf(t *testing.T, in *ecs.RunTaskInput) map[string]string {
	t.Helper()
	if in.Overrides == nil || len(in.Overrides.ContainerOverrides) != 1 {
		t.Fatalf("overrides = %+v, want exactly one container override", in.Overrides)
	}
	override := in.Overrides.ContainerOverrides[0]
	if aws.ToString(override.Name) != ScannerContainerName {
		t.Fatalf("override container = %q, want %q", aws.ToString(override.Name), ScannerContainerName)
	}
	env := map[string]string{}
	for _, pair := range override.Environment {
		env[aws.ToString(pair.Name)] = aws.ToString(pair.Value)
	}
	return env
}

func TestHome_RunTaskBuildsOverrides(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		api := &recordingECS{}
		arn, err := newHome(api, nil, testHomeConfig).RunTask(context.Background(), LaunchSpec{RunID: 42, Mode: "scan"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(arn, "/abc") {
			t.Fatalf("task arn = %q", arn)
		}
		in := api.called(t)
		if aws.ToString(in.Cluster) != testHomeConfig.ClusterARN || aws.ToString(in.TaskDefinition) != testHomeConfig.TaskDefinitionARN {
			t.Fatalf("cluster %q, task definition %q", aws.ToString(in.Cluster), aws.ToString(in.TaskDefinition))
		}
		if in.LaunchType != ecstypes.LaunchTypeFargate || in.EnableExecuteCommand {
			t.Fatalf("launch type %q, exec %v; want FARGATE without exec", in.LaunchType, in.EnableExecuteCommand)
		}
		vpc := in.NetworkConfiguration.AwsvpcConfiguration
		if strings.Join(vpc.Subnets, ",") != "subnet-aaa,subnet-bbb" || strings.Join(vpc.SecurityGroups, ",") != testHomeConfig.SecurityGroup ||
			vpc.AssignPublicIp != ecstypes.AssignPublicIpDisabled {
			t.Fatalf("network = %+v, want the configured subnets and group with no public IP", vpc)
		}
		if in.PropagateTags != ecstypes.PropagateTagsTaskDefinition {
			t.Fatalf("propagate tags = %q, want TASK_DEFINITION", in.PropagateTags)
		}
		if len(in.Tags) != 1 || aws.ToString(in.Tags[0].Key) != "harbormaster:scan-run" || aws.ToString(in.Tags[0].Value) != "42" {
			t.Fatalf("tags = %+v, want harbormaster:scan-run=42", in.Tags)
		}
		env := environmentOf(t, in)
		if len(env) != 2 || env["HM_SCAN_RUN_ID"] != "42" || env["HM_SCAN_MODE"] != "scan" {
			t.Fatalf("environment = %v, want only HM_SCAN_RUN_ID=42 and HM_SCAN_MODE=scan", env)
		}
		override := in.Overrides.ContainerOverrides[0]
		if in.Overrides.TaskRoleArn != nil || in.Overrides.ExecutionRoleArn != nil || in.Overrides.Cpu != nil ||
			in.Overrides.Memory != nil || in.Overrides.EphemeralStorage != nil || override.Cpu != nil || override.Memory != nil {
			t.Fatalf("overrides = %+v, want no role, size or storage override on a normal run", in.Overrides)
		}
	})

	t.Run("large", func(t *testing.T) {
		api := &recordingECS{}
		if _, err := newHome(api, nil, testHomeConfig).RunTask(context.Background(), LaunchSpec{RunID: 7, Mode: "scan", Large: true}); err != nil {
			t.Fatal(err)
		}
		o := api.called(t).Overrides
		if o == nil {
			t.Fatal("no overrides sent")
		}
		if aws.ToString(o.Cpu) != "2048" || aws.ToString(o.Memory) != "4096" ||
			o.EphemeralStorage == nil || o.EphemeralStorage.SizeInGiB != 60 {
			t.Fatalf("overrides = %+v, want cpu 2048, memory 4096, ephemeral 60", o)
		}
		if o.TaskRoleArn != nil {
			t.Fatalf("task role override = %q, want none", aws.ToString(o.TaskRoleArn))
		}
		if env := environmentOf(t, api.called(t)); env["HM_SCAN_RUN_ID"] != "7" {
			t.Fatalf("environment = %v", env)
		}
	})

	t.Run("a placement failure is an error", func(t *testing.T) {
		api := &failingRunECS{}
		if _, err := newHome(api, nil, testHomeConfig).RunTask(context.Background(), LaunchSpec{RunID: 1, Mode: "scan"}); err == nil {
			t.Fatal("RunTask succeeded although ECS returned only a failure")
		}
	})
}

type describingECS struct {
	recordingECS
	out *ecs.DescribeTasksOutput
}

func (d *describingECS) DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	return d.out, nil
}

func TestHome_DescribeTaskTellsMissingFromOtherFailures(t *testing.T) {
	describe := func(out *ecs.DescribeTasksOutput) error {
		_, err := newHome(&describingECS{out: out}, nil, testHomeConfig).DescribeTask(context.Background(), "arn:task")
		return err
	}
	if err := describe(&ecs.DescribeTasksOutput{Failures: []ecstypes.Failure{{Reason: aws.String("MISSING")}}}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("MISSING gave %v, want ErrTaskNotFound", err)
	}
	if err := describe(&ecs.DescribeTasksOutput{}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("empty answer gave %v, want ErrTaskNotFound", err)
	}
	err := describe(&ecs.DescribeTasksOutput{Failures: []ecstypes.Failure{{Reason: aws.String("ACCESS_DENIED")}}})
	if err == nil || errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("other failure gave %v, want an error that is not ErrTaskNotFound", err)
	}
}

type failingRunECS struct{ recordingECS }

func (failingRunECS) RunTask(context.Context, *ecs.RunTaskInput, ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	return &ecs.RunTaskOutput{Failures: []ecstypes.Failure{{Reason: aws.String("RESOURCE:FARGATE")}}}, nil
}

// recordingS3 fakes the S3 calls the Home bucket makes.
type recordingS3 struct {
	mu          sync.Mutex
	parts       [][]byte
	completed   int
	aborted     int
	abortCtxErr error
	onPart      func(partNumber int32)
	abortErr    error

	getBody io.Reader
	getErr  error
	putSSE  []s3types.ServerSideEncryption
}

func (s *recordingS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	s.putSSE = append(s.putSSE, in.ServerSideEncryption)
	return &s3.PutObjectOutput{}, nil
}

func (s *recordingS3) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(s.getBody)}, nil
}

func (s *recordingS3) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, nil
}

func (s *recordingS3) CreateMultipartUpload(_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	s.putSSE = append(s.putSSE, in.ServerSideEncryption)
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload-1")}, nil
}

func (s *recordingS3) UploadPart(ctx context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.parts = append(s.parts, data)
	s.mu.Unlock()
	if s.onPart != nil {
		s.onPart(aws.ToInt32(in.PartNumber))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &s3.UploadPartOutput{ETag: aws.String("etag")}, nil
}

func (s *recordingS3) CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	s.completed++
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (s *recordingS3) AbortMultipartUpload(ctx context.Context, _ *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	s.aborted++
	s.abortCtxErr = ctx.Err()
	if s.abortErr != nil {
		return nil, s.abortErr
	}
	return &s3.AbortMultipartUploadOutput{}, nil
}

// archiveOfSeventeenMiB spans two parts: one full 16 MiB buffer and a 1 MiB tail.
func archiveOfSeventeenMiB() []byte { return bytes.Repeat([]byte{0x5a}, 17<<20) }

func TestHome_PutStreamAbortsWhenVerifyFails(t *testing.T) {
	errMismatch := errors.New("checksum mismatch")

	t.Run("verify fails", func(t *testing.T) {
		api := &recordingS3{}
		err := newHome(nil, api, testHomeConfig).PutStream(context.Background(), "grype-db/v6/x.tar.zst",
			bytes.NewReader(archiveOfSeventeenMiB()), func() error { return errMismatch })
		if !errors.Is(err, errMismatch) {
			t.Fatalf("err = %v, want the verify error", err)
		}
		if api.completed != 0 || api.aborted != 1 {
			t.Fatalf("completed %d, aborted %d; want the upload aborted and never completed", api.completed, api.aborted)
		}
	})

	t.Run("verify passes", func(t *testing.T) {
		api := &recordingS3{}
		err := newHome(nil, api, testHomeConfig).PutStream(context.Background(), "grype-db/v6/x.tar.zst",
			bytes.NewReader(archiveOfSeventeenMiB()), func() error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if api.completed != 1 || api.aborted != 0 {
			t.Fatalf("completed %d, aborted %d; want one completion", api.completed, api.aborted)
		}
		if len(api.parts) != 2 || len(api.parts[0]) != 16<<20 || len(api.parts[1]) != 1<<20 {
			t.Fatalf("part sizes = %v, want 16 MiB then 1 MiB", partSizes(api.parts))
		}
		for _, sse := range api.putSSE {
			if sse != s3types.ServerSideEncryptionAes256 {
				t.Fatalf("server-side encryption = %q, want AES256", sse)
			}
		}
	})

	t.Run("a failed abort is reported with the cause", func(t *testing.T) {
		errAbort := errors.New("abort refused")
		api := &recordingS3{abortErr: errAbort}
		err := newHome(nil, api, testHomeConfig).PutStream(context.Background(), "grype-db/v6/x.tar.zst",
			bytes.NewReader([]byte("x")), func() error { return errMismatch })
		if !errors.Is(err, errMismatch) || !errors.Is(err, errAbort) {
			t.Fatalf("err = %v, want both the verify error and the abort error", err)
		}
	})

	t.Run("cancelled caller still aborts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		api := &recordingS3{onPart: func(int32) { cancel() }}
		err := newHome(nil, api, testHomeConfig).PutStream(ctx, "grype-db/v6/x.tar.zst",
			bytes.NewReader(archiveOfSeventeenMiB()), func() error { return nil })
		if err == nil {
			t.Fatal("PutStream succeeded although the caller was cancelled")
		}
		if api.completed != 0 || api.aborted != 1 {
			t.Fatalf("completed %d, aborted %d; want one abort", api.completed, api.aborted)
		}
		if api.abortCtxErr != nil {
			t.Fatalf("abort ran with a cancelled context: %v", api.abortCtxErr)
		}
	})
}

func partSizes(parts [][]byte) []int {
	sizes := make([]int, len(parts))
	for i, p := range parts {
		sizes[i] = len(p)
	}
	return sizes
}

type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestHome_GetCapsSize(t *testing.T) {
	get := func(api *recordingS3, limit int64) ([]byte, error) {
		return newHome(nil, api, testHomeConfig).Get(context.Background(), "results/1/manifest.json", limit)
	}

	t.Run("a body at the cap is returned", func(t *testing.T) {
		data, err := get(&recordingS3{getBody: strings.NewReader("12345")}, 5)
		if err != nil || string(data) != "12345" {
			t.Fatalf("got %q, %v; want the body", data, err)
		}
	})

	t.Run("one byte over the cap is too large", func(t *testing.T) {
		_, err := get(&recordingS3{getBody: strings.NewReader("123456")}, 5)
		if !errors.Is(err, ErrObjectTooLarge) {
			t.Fatalf("err = %v, want ErrObjectTooLarge", err)
		}
	})

	t.Run("an endless body is cut off", func(t *testing.T) {
		_, err := get(&recordingS3{getBody: endlessReader{}}, 1024)
		if !errors.Is(err, ErrObjectTooLarge) {
			t.Fatalf("err = %v, want ErrObjectTooLarge", err)
		}
	})

	t.Run("a missing key", func(t *testing.T) {
		_, err := get(&recordingS3{getErr: &s3types.NoSuchKey{}}, 5)
		if !errors.Is(err, ErrObjectNotFound) {
			t.Fatalf("err = %v, want ErrObjectNotFound", err)
		}
	})

	t.Run("access denied stays an error", func(t *testing.T) {
		denied := &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
		_, err := get(&recordingS3{getErr: denied}, 5)
		if err == nil || errors.Is(err, ErrObjectNotFound) {
			t.Fatalf("err = %v, want a plain error that is not ErrObjectNotFound", err)
		}
	})
}
