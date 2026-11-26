package mcpgw

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
)

// unavailableMessage is the only thing the model learns about a transport
// failure, a timeout or a 5xx; the detail goes to the gateway's log.
const unavailableMessage = "Harbormaster is unavailable. Try again shortly."

// textResult is a successful tool result holding the upstream JSON body.
func textResult(body []byte) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}
}

// toolError is a tool result with isError set, shown to the model.
func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

// tooLargeResult tells the model how to get a result under the cap instead of
// returning truncated (and therefore invalid) JSON.
func tooLargeResult(size int) *mcp.CallToolResult {
	return toolError(fmt.Sprintf("The result is too large (%d bytes). Narrow the filters or lower `limit`.", size))
}

// upstreamErrorResult maps a non-2xx, non-401 upstream answer to a tool error
// (design, "Mapping"). 401 is handled by the caller: it becomes HTTP 401.
func upstreamErrorResult(resp hmtools.Response) *mcp.CallToolResult {
	if resp.Status >= 500 || (resp.Status >= 300 && resp.Status < 400) {
		return toolError(unavailableMessage)
	}
	message := strings.TrimSpace(resp.ErrorMessage)
	if message == "" {
		message = defaultStatusMessage(resp.Status)
	}
	if resp.ErrorCode != "" {
		message += " (" + resp.ErrorCode + ")"
	}
	if retry := retryAfterText(resp.RetryAfter); retry != "" {
		message += " " + retry
	}
	return toolError(message)
}

func defaultStatusMessage(status int) string {
	switch status {
	case http.StatusForbidden:
		return "Harbormaster refused this request for the signed-in user."
	case http.StatusNotFound:
		return "Not found, or not visible to the signed-in user."
	case http.StatusConflict:
		return "Harbormaster reported a conflict."
	case http.StatusUnprocessableEntity:
		return "Harbormaster rejected the arguments."
	case http.StatusTooManyRequests:
		return "Too many requests."
	}
	return fmt.Sprintf("Harbormaster refused the request (HTTP %d).", status)
}

// retryAfterText renders a Retry-After header for the model.
func retryAfterText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return fmt.Sprintf("Retry after %d seconds.", seconds)
	}
	return "Retry after " + value + "."
}
