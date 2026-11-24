package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/hmtools"
)

// ToolRunner runs one tool call for a run and returns text for the model.
type ToolRunner interface {
	Run(ctx context.Context, bearer, name string, args map[string]any) (text string, isError bool)
}

const unavailableMessage = "Harbormaster is unavailable. Try again shortly."

var toolTable = func() map[string]hmtools.Tool {
	table := map[string]hmtools.Tool{}
	for _, tool := range hmtools.Tools() {
		table[tool.Name] = tool
	}
	return table
}()

// lookupTool finds a tool in the shared read-only table. No other request to
// Harbormaster is possible.
func lookupTool(name string) (hmtools.Tool, bool) {
	tool, ok := toolTable[name]
	return tool, ok
}

type upstreamToolRunner struct{ up *hmtools.Upstream }

// NewUpstreamToolRunner runs tools against Harbormaster through up: one tool
// call is one upstream request, built by the tool's own BuildCall. It bounds
// how much of an answer is read to maxResultBytes (see hmtools.UpstreamReadCap),
// so call it before up serves other calls.
func NewUpstreamToolRunner(up *hmtools.Upstream, maxResultBytes int) ToolRunner {
	up.SetReadCap(maxResultBytes)
	return upstreamToolRunner{up: up}
}

func (r upstreamToolRunner) Run(ctx context.Context, bearer, name string, args map[string]any) (string, bool) {
	tool, ok := lookupTool(name)
	if !ok {
		return unknownToolMessage(name), true
	}
	call, err := tool.BuildCall(args)
	if err != nil {
		return buildErrorMessage(err), true
	}
	resp, err := r.up.Do(ctx, bearer, "", call)
	if err != nil {
		return unavailableMessage, true
	}
	switch {
	case resp.Status >= 200 && resp.Status <= 299:
		return string(resp.Body), false
	case resp.Status >= 500 || (resp.Status >= 300 && resp.Status < 400):
		return unavailableMessage, true
	}
	return upstreamErrorMessage(resp), true
}

func unknownToolMessage(name string) string {
	if len(name) > 64 {
		name = name[:64]
	}
	return fmt.Sprintf("There is no tool named %q. Use only the tools you were given.", name)
}

// buildErrorMessage is what the model learns about a call that was not made.
func buildErrorMessage(err error) string {
	var argErr *hmtools.ArgumentError
	if errors.As(err, &argErr) {
		return "The arguments were not valid: " + argErr.Message
	}
	return "The tool could not be called."
}

func upstreamErrorMessage(resp hmtools.Response) string {
	message := strings.TrimSpace(resp.ErrorMessage)
	if message == "" {
		switch resp.Status {
		case http.StatusUnauthorized:
			message = "Harbormaster did not accept this run's access."
		case http.StatusForbidden:
			message = "Harbormaster refused this request for the signed-in user."
		case http.StatusNotFound:
			message = "Not found, or not visible to the signed-in user."
		case http.StatusTooManyRequests:
			message = "Too many requests."
		default:
			message = fmt.Sprintf("Harbormaster refused the request (HTTP %d).", resp.Status)
		}
	}
	if resp.ErrorCode != "" {
		message += " (" + resp.ErrorCode + ")"
	}
	if retry := strings.TrimSpace(resp.RetryAfter); retry != "" {
		if seconds, err := strconv.Atoi(retry); err == nil && seconds >= 0 {
			message += fmt.Sprintf(" Retry after %d seconds.", seconds)
		} else {
			message += " Retry after " + retry + "."
		}
	}
	return message
}
