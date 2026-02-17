package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type TelegramNotifier struct {
	enabled  bool
	botToken string
	chatID   string
	baseURL  string
	client   *http.Client
}

func NewTelegramNotifier(enabled bool, botToken, chatID, baseURL string, timeout time.Duration) *TelegramNotifier {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &TelegramNotifier{
		enabled:  enabled,
		botToken: botToken,
		chatID:   chatID,
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   &http.Client{Timeout: timeout},
	}
}

func (t *TelegramNotifier) Notify(ctx context.Context, msg string) error {
	if t == nil || !t.enabled {
		return nil
	}
	body, err := json.Marshal(telegramSendMessageRequest{
		ChatID: t.chatID,
		Text:   msg,
	})
	if err != nil {
		return err
	}
	endpoint := t.baseURL + "/bot" + t.botToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if len(respBody) == 0 {
		return nil
	}
	var parsed telegramSendMessageResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil
	}
	if !parsed.OK {
		return fmt.Errorf("telegram api error: %s", strings.TrimSpace(parsed.Description))
	}
	return nil
}

type telegramSendMessageRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type telegramSendMessageResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}
