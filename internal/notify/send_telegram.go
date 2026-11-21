package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

const defaultTelegramBase = "https://api.telegram.org"

var (
	telegramBotToken = regexp.MustCompile(`^[0-9]{6,12}:[A-Za-z0-9_-]{30,64}$`)
	telegramChatID   = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
)

func (s *Senders) telegramBase() string {
	if s.TelegramBase != "" {
		return s.TelegramBase
	}
	return defaultTelegramBase
}

// telegramAnswer is the part of the Bot API's reply that is read. The description is never read: it can repeat
// the request, and so the token.
type telegramAnswer struct {
	OK         bool `json:"ok"`
	ErrorCode  int  `json:"error_code"`
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// sendTelegram posts to {base}/bot<token>/sendMessage. The token is in the URL path, so the request URL and any
// error from the client are never logged or returned (see postJSON); the note is a fixed sentence per status.
func (s *Senders) sendTelegram(ctx context.Context, r store.NotificationRoute, secret Secret, m Message) Result {
	body, err := TelegramBody(m, r.Config.ChatID, r.Config.ThreadID)
	if err != nil {
		return Result{Note: "The message could not be built."}
	}
	reply, res, ok := s.postJSON(ctx, s.telegramBase()+"/bot"+secret.BotToken+"/sendMessage", body, m, nil)
	if !ok {
		return res
	}
	return telegramResult(reply, time.Now())
}

func telegramResult(reply httpReply, now time.Time) Result {
	res := Result{StatusCode: reply.status, Duration: reply.duration}
	var answer telegramAnswer
	_ = json.Unmarshal(reply.body, &answer)
	switch {
	case reply.status == http.StatusOK && answer.OK:
		res.Delivered = true
		res.Note = fmt.Sprintf("Delivered in %d ms", reply.duration.Milliseconds())
	case reply.status == http.StatusOK:
		res.Note = "Telegram returned an unexpected answer."
	case reply.status == http.StatusTooManyRequests:
		res.Retryable = true
		res.Note = "Telegram rate limit"
		if answer.Parameters.RetryAfter > 0 {
			res.RetryAfter = min(time.Duration(answer.Parameters.RetryAfter)*time.Second, maxRetryAfter)
		} else {
			res.RetryAfter = parseRetryAfter(reply.header.Get("Retry-After"), now)
		}
	case reply.status == http.StatusUnauthorized, reply.status == http.StatusNotFound:
		res.Note = "Telegram rejected the bot token."
	case reply.status == http.StatusBadRequest:
		res.Note = "Telegram rejected the message or the chat ID."
	case reply.status == http.StatusForbidden:
		res.Note = "The bot cannot post to this chat. Add it to the chat first."
	case reply.status >= 500:
		res.Retryable = true
		res.Note = fmt.Sprintf("HTTP %d", reply.status)
	default:
		res.Note = fmt.Sprintf("HTTP %d", reply.status)
	}
	return res
}

func (s *Senders) validateTelegram(cfg store.NotificationConfig, secret Secret) error {
	if !telegramBotToken.MatchString(secret.BotToken) {
		return errors.New("Enter the bot token BotFather gave you.")
	}
	if !telegramChatID.MatchString(cfg.ChatID) {
		return errors.New("Enter a numeric chat ID or @channelname.")
	}
	if cfg.ThreadID < 0 || cfg.ThreadID > 2147483647 {
		return errors.New("Enter a topic ID, or leave it empty.")
	}
	return nil
}
