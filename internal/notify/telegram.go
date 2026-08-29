package notify

import (
	"fmt"
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
