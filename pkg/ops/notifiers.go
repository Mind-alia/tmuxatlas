package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PushFunc sends one payload to every browser subscription. pkg/webpush
// provides it; subscriptions can only be created by admins.
type PushFunc func(ctx context.Context, payload any, ttl time.Duration, urgency string) error

// WebPushNotifier sends ops notifications as Web Push messages to the PWA.
type WebPushNotifier struct {
	Push PushFunc
}

func (w WebPushNotifier) Name() string { return "webpush" }

// webPushPayload matches what web/public/sw.js reads.
type webPushPayload struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	Tag    string `json:"tag"`
	Status string `json:"status"`
}

func (w WebPushNotifier) Notify(ctx context.Context, n Notification) error {
	tag := n.Key // the recovery replaces the outage on the phone
	if n.Kind == "group" || n.Kind == "test" {
		tag = n.Key + ":" + n.At.Format(time.RFC3339)
	}
	return w.Push(ctx, webPushPayload{Title: n.Title, Body: n.Body, URL: "/ops", Tag: tag, Status: n.Kind},
		time.Hour, "high")
}

// NtfyNotifier publishes to an ntfy topic (https://ntfy.sh or self-hosted).
// The topic URL and the token come from the environment (.env), never from
// the browser.
type NtfyNotifier struct {
	TopicURL string // https://ntfy.sh/<topic>
	Token    string // optional access token
	ClickURL string // opened when the notification is tapped
	Client   *http.Client
}

// NewNtfyNotifier validates the topic URL.
func NewNtfyNotifier(topicURL, token, clickURL string) (*NtfyNotifier, error) {
	u, err := url.Parse(strings.TrimSpace(topicURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("TMUXATLAS_NTFY_URL invalide : https://serveur/sujet attendu")
	}
	topic := strings.Trim(u.Path, "/")
	if topic == "" || strings.Contains(topic, "/") {
		return nil, fmt.Errorf("TMUXATLAS_NTFY_URL doit nommer un seul sujet : https://serveur/sujet")
	}
	if u.Scheme == "http" && token != "" {
		return nil, fmt.Errorf("TMUXATLAS_NTFY_TOKEN refusé sur une URL http")
	}
	return &NtfyNotifier{TopicURL: u.Scheme + "://" + u.Host + "/" + topic, Token: strings.TrimSpace(token), ClickURL: clickURL,
		Client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (n *NtfyNotifier) Name() string { return "ntfy" }

var ntfyPriority = map[string]int{"critical": 5, "warning": 4, "info": 3}
var ntfyTags = map[string][]string{"critical": {"rotating_light"}, "warning": {"warning"}, "info": {"white_check_mark"}}

func (n *NtfyNotifier) Notify(ctx context.Context, msg Notification) error {
	u, _ := url.Parse(n.TopicURL)
	topic := strings.Trim(u.Path, "/")
	body := map[string]any{
		"topic": topic, "title": msg.Title, "message": msg.Body,
		"priority": ntfyPriority[msg.Severity], "tags": ntfyTags[msg.Severity],
	}
	if msg.Body == "" {
		body["message"] = msg.Title
	}
	if n.ClickURL != "" {
		body["click"] = n.ClickURL
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	// JSON publishing goes to the server root, which keeps UTF-8 titles intact.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.Scheme+"://"+u.Host+"/", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	client := n.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy : HTTP %d", resp.StatusCode)
	}
	return nil
}
