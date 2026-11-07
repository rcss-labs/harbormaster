package awsx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Own-account clients: Harbormaster's own ECS cluster (to start the scanner
// task) and its own scan bucket. They use the server's task role, never an
// assumed harbormaster-access role.

var (
	// ErrObjectNotFound means the bucket has no object under the key.
	ErrObjectNotFound = errors.New("object not found")
	// ErrObjectTooLarge means the object is bigger than the caller's cap.
	ErrObjectTooLarge = errors.New("object larger than the allowed size")
	// ErrTaskNotFound means ECS no longer knows the task.
	ErrTaskNotFound = errors.New("task not found")
)

// ScannerContainerName is the container in the scanner task definition that
// receives the per-run environment overrides.
const ScannerContainerName = "scanner"

const (
	scanRunTagKey = "harbormaster:scan-run"

	// largeCPU, largeMemoryMiB and largeEphemeralGiB are the sizes of the retry
	// after a timeout.
	largeCPU          = "2048"
	largeMemoryMiB    = "4096"
	largeEphemeralGiB = 60

	// uploadPartBytes is the multipart part size; the buffer is allocated once.
	uploadPartBytes = 16 << 20
	abortTimeout    = time.Minute
)

// TaskState is what ECS reports about one task.
type TaskState struct {
	LastStatus    string // PROVISIONING, PENDING, RUNNING, DEACTIVATING, STOPPING, DEPROVISIONING, STOPPED
	StopCode      string
	StoppedReason string
	ExitCode      *int32 // of the scanner container, once it has stopped
	StartedAt     time.Time
	StoppedAt     time.Time
}

// LaunchSpec describes one scanner task.
type LaunchSpec struct {
	RunID int64
	Mode  string
	Large bool
}

// Launcher starts and watches scanner tasks.
type Launcher interface {
	RunTask(ctx context.Context, spec LaunchSpec) (taskARN string, err error)
	// DescribeTask returns ErrTaskNotFound when ECS no longer knows the task.
	DescribeTask(ctx context.Context, taskARN string) (TaskState, error)
	StopTask(ctx context.Context, taskARN, reason string) error
}

// Bucket is the scan bucket.
type Bucket interface {
	Put(ctx context.Context, key string, body []byte) error
	// Get returns ErrObjectNotFound for a missing key and ErrObjectTooLarge
	// for an object over maxBytes.
	Get(ctx context.Context, key string, maxBytes int64) ([]byte, error)
	Delete(ctx context.Context, key string) error
	// PutStream uploads body by multipart and completes the object only when
	// the stream ended cleanly and verify returns nil; otherwise it aborts.
	PutStream(ctx context.Context, key string, body io.Reader, verify func() error) error
}

// HomeConfig locates the scanner task and the bucket.
type HomeConfig struct {
	ClusterARN, TaskDefinitionARN, SecurityGroup, Bucket string
	Subnets                                              []string
}

type homeECS interface {
	RunTask(ctx context.Context, in *ecs.RunTaskInput, opts ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
	DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	StopTask(ctx context.Context, in *ecs.StopTaskInput, opts ...func(*ecs.Options)) (*ecs.StopTaskOutput, error)
}

type homeS3 interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, opts ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(ctx context.Context, in *s3.UploadPartInput, opts ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, opts ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, opts ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

// Home implements Launcher and Bucket against Harbormaster's own account.
type Home struct {
	ecs    homeECS
	s3     homeS3
	config HomeConfig
}

// NewHome builds the clients from the server's own AWS config.
func NewHome(base aws.Config, cfg HomeConfig) *Home {
	return newHome(ecs.NewFromConfig(base), s3.NewFromConfig(base), cfg)
}

func newHome(ecsClient homeECS, s3Client homeS3, cfg HomeConfig) *Home {
	return &Home{ecs: ecsClient, s3: s3Client, config: cfg}
}

var (
	_ Launcher = (*Home)(nil)
	_ Bucket   = (*Home)(nil)
)

// RunTask starts one scanner task: Fargate, private networking, no exec, and
// the task definition's own roles. Only the run id and mode are passed in.
func (h *Home) RunTask(ctx context.Context, spec LaunchSpec) (string, error) {
	runID := strconv.FormatInt(spec.RunID, 10)
	overrides := &ecstypes.TaskOverride{
		ContainerOverrides: []ecstypes.ContainerOverride{{
			Name: aws.String(ScannerContainerName),
			Environment: []ecstypes.KeyValuePair{
				{Name: aws.String("HM_SCAN_RUN_ID"), Value: aws.String(runID)},
				{Name: aws.String("HM_SCAN_MODE"), Value: aws.String(spec.Mode)},
			},
		}},
	}
	if spec.Large {
		overrides.Cpu = aws.String(largeCPU)
		overrides.Memory = aws.String(largeMemoryMiB)
		overrides.EphemeralStorage = &ecstypes.EphemeralStorage{SizeInGiB: largeEphemeralGiB}
	}
	out, err := h.ecs.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(h.config.ClusterARN),
		TaskDefinition: aws.String(h.config.TaskDefinitionARN),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
			Subnets:        h.config.Subnets,
			SecurityGroups: []string{h.config.SecurityGroup},
			AssignPublicIp: ecstypes.AssignPublicIpDisabled,
		}},
		EnableExecuteCommand: false,
		PropagateTags:        ecstypes.PropagateTagsTaskDefinition,
		Tags:                 []ecstypes.Tag{{Key: aws.String(scanRunTagKey), Value: aws.String(runID)}},
		Overrides:            overrides,
	})
	if err != nil {
		return "", err
	}
	if len(out.Tasks) == 0 {
		reason := "no task started"
		if len(out.Failures) > 0 {
			reason = aws.ToString(out.Failures[0].Reason)
		}
		return "", fmt.Errorf("run scanner task: %s", reason)
	}
	return aws.ToString(out.Tasks[0].TaskArn), nil
}

// DescribeTask reports a task's state, or ErrTaskNotFound when ECS has none.
func (h *Home) DescribeTask(ctx context.Context, taskARN string) (TaskState, error) {
	out, err := h.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(h.config.ClusterARN), Tasks: []string{taskARN}})
	if err != nil {
		return TaskState{}, err
	}
	if len(out.Tasks) == 0 {
		if len(out.Failures) == 0 {
			return TaskState{}, ErrTaskNotFound
		}
		if reason := aws.ToString(out.Failures[0].Reason); reason != "MISSING" {
			return TaskState{}, fmt.Errorf("describe scanner task: %s", reason)
		}
		return TaskState{}, ErrTaskNotFound
	}
	task := out.Tasks[0]
	state := TaskState{
		LastStatus:    aws.ToString(task.LastStatus),
		StopCode:      string(task.StopCode),
		StoppedReason: aws.ToString(task.StoppedReason),
		StartedAt:     aws.ToTime(task.StartedAt),
		StoppedAt:     aws.ToTime(task.StoppedAt),
	}
	for _, container := range task.Containers {
		if aws.ToString(container.Name) == ScannerContainerName {
			state.ExitCode = container.ExitCode
		}
	}
	return state, nil
}

func (h *Home) StopTask(ctx context.Context, taskARN, reason string) error {
	_, err := h.ecs.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(h.config.ClusterARN), Task: aws.String(taskARN), Reason: aws.String(reason)})
	return err
}

func (h *Home) Put(ctx context.Context, key string, body []byte) error {
	_, err := h.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(h.config.Bucket),
		Key:                  aws.String(key),
		Body:                 bytes.NewReader(body),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	})
	return err
}

// Get reads maxBytes+1 bytes so that an over-cap object is told apart from
// one that is exactly at the cap.
func (h *Home) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	out, err := h.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(h.config.Bucket), Key: aws.String(key)})
	if err != nil {
		var missing *s3types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, ErrObjectNotFound
		}
		return nil, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrObjectTooLarge
	}
	return data, nil
}

func (h *Home) Delete(ctx context.Context, key string) error {
	_, err := h.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(h.config.Bucket), Key: aws.String(key)})
	return err
}

func (h *Home) PutStream(ctx context.Context, key string, body io.Reader, verify func() error) (err error) {
	bucket, objectKey := aws.String(h.config.Bucket), aws.String(key)
	created, err := h.s3.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: bucket, Key: objectKey, ServerSideEncryption: s3types.ServerSideEncryptionAes256})
	if err != nil {
		return err
	}
	uploadID := created.UploadId
	defer func() {
		if err == nil {
			return
		}
		// The caller's context may be what failed, so abort on a fresh one.
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
		defer cancel()
		if _, abortErr := h.s3.AbortMultipartUpload(abortCtx, &s3.AbortMultipartUploadInput{Bucket: bucket, Key: objectKey, UploadId: uploadID}); abortErr != nil {
			err = errors.Join(err, fmt.Errorf("abort multipart upload: %w", abortErr))
		}
	}()

	buffer := make([]byte, uploadPartBytes)
	var parts []s3types.CompletedPart
	for partNumber := int32(1); ; partNumber++ {
		n, readErr := io.ReadFull(body, buffer)
		atEnd := readErr == io.EOF || readErr == io.ErrUnexpectedEOF
		if readErr != nil && !atEnd {
			return readErr
		}
		// S3 needs at least one part, so an empty stream still sends one.
		if n > 0 || partNumber == 1 {
			uploaded, uploadErr := h.s3.UploadPart(ctx, &s3.UploadPartInput{
				Bucket: bucket, Key: objectKey, UploadId: uploadID,
				PartNumber: aws.Int32(partNumber), Body: bytes.NewReader(buffer[:n]),
			})
			if uploadErr != nil {
				return uploadErr
			}
			parts = append(parts, s3types.CompletedPart{ETag: uploaded.ETag, PartNumber: aws.Int32(partNumber)})
		}
		if atEnd {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verify(); err != nil {
		return err
	}
	_, err = h.s3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: bucket, Key: objectKey, UploadId: uploadID,
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	})
	return err
}
