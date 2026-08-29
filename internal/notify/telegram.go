package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google-automation/internal/analytics"
)

type Telegram struct {
	botToken string
	chatID   string
	client   *http.Client
}

func NewTelegram(botToken, chatID string) *Telegram {
	return &Telegram{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *Telegram) Send(text string) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)
	resp, err := t.client.PostForm(endpoint, url.Values{
		"chat_id":    {t.chatID},
		"text":       {text},
		"parse_mode": {"HTML"},
	})
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram send: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (t *Telegram) SendDailySummary(s *analytics.Summary) error {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>📊 Daily Report — %s</b>\n\n", s.Date))
	sb.WriteString(fmt.Sprintf("Total searches : <b>%d</b>\n", s.TotalSearch))
	sb.WriteString(fmt.Sprintf("Successful     : <b>%d</b>\n", s.Success))
	sb.WriteString(fmt.Sprintf("Failed         : <b>%d</b>\n", s.Fail))
	sb.WriteString(fmt.Sprintf("CAPTCHAs       : <b>%d</b>\n", s.Captcha))
	sb.WriteString(fmt.Sprintf("Success rate   : <b>%.1f%%</b>\n", s.SuccessRate))
	sb.WriteString(fmt.Sprintf("CAPTCHA rate   : <b>%.1f%%</b>\n", s.CaptchaRate))
	sb.WriteString(fmt.Sprintf("Avg dwell      : <b>%.1fs</b>\n", s.AvgDwellSeconds))
	sb.WriteString(fmt.Sprintf("Avg SERP pos   : <b>%.1f</b>", s.AvgSerpPosition))
	return t.Send(sb.String())
}

// CommandHandlers defines callback functions for incoming Telegram bot commands.
type CommandHandlers struct {
	OnStatus func() string
	OnStats  func() string
	OnPause  func() string
	OnResume func() string
}

type tgUpdate struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

type tgUpdatesResponse struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

// StartCommandListener listens for incoming Telegram commands in the background.
func (t *Telegram) StartCommandListener(handlers CommandHandlers) {
	go func() {
		offset := 0
		for {
			endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=20", t.botToken, offset)
			resp, err := t.client.Get(endpoint)
			if err != nil {
				time.Sleep(5 * time.Second)
				continue
			}

			var updatesResp tgUpdatesResponse
			importJSONErr := jsonDecode(resp.Body, &updatesResp)
			_ = resp.Body.Close()

			if importJSONErr == nil && updatesResp.OK {
				for _, u := range updatesResp.Result {
					if u.UpdateID >= offset {
						offset = u.UpdateID + 1
					}
					if u.Message == nil {
						continue
					}

					cmd := strings.TrimSpace(u.Message.Text)
					var reply string

					switch {
					case strings.HasPrefix(cmd, "/status"):
						if handlers.OnStatus != nil {
							reply = handlers.OnStatus()
						}
					case strings.HasPrefix(cmd, "/stats"):
						if handlers.OnStats != nil {
							reply = handlers.OnStats()
						}
					case strings.HasPrefix(cmd, "/pause"):
						if handlers.OnPause != nil {
							reply = handlers.OnPause()
						}
					case strings.HasPrefix(cmd, "/resume"):
						if handlers.OnResume != nil {
							reply = handlers.OnResume()
						}
					case strings.HasPrefix(cmd, "/start") || strings.HasPrefix(cmd, "/help"):
						reply = "<b>🤖 Google Automation Controller</b>\n\nCommands:\n• <code>/status</code> — Current task & proxy pool status\n• <code>/stats</code> — Today's search & SERP analytics\n• <code>/pause</code> — Pause search engine loop\n• <code>/resume</code> — Resume search loop"
					}

					if reply != "" {
						_ = t.Send(reply)
					}
				}
			}
			time.Sleep(1 * time.Second)
		}
	}()
}

func jsonDecode(r io.Reader, v interface{}) error {
	return json.NewDecoder(r).Decode(v)
}
