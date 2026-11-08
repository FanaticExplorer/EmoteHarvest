package harvest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	apiBase   = "https://discord.com/api/v10"
	cdnBase   = "https://cdn.discordapp.com"
	mediaBase = "https://media.discordapp.net"

	userAgent = "EmoteHarvest/1.0 (+https://github.com/FanaticExplorer/EmoteHarvest)"
)

var (
	guildIDPattern = regexp.MustCompile(`^\d{17,20}$`)
	invitePattern  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

type Emoji struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Animated bool   `json:"animated"`
}

type Sticker struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	FormatType int    `json:"format_type"`
}

type GuildData struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Emojis   []Emoji   `json:"emojis"`
	Stickers []Sticker `json:"stickers"`
}

type inviteResponse struct {
	Guild struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"guild"`
}

type guildPreview struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Emojis   []Emoji   `json:"emojis"`
	Stickers []Sticker `json:"stickers"`
}

type Client struct {
	http  *http.Client
	token string
}

func NewClient(token string) *Client {
	token = strings.TrimSpace(token)
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bot "))

	return &Client{
		http:  &http.Client{Timeout: 30 * time.Second},
		token: token,
	}
}

func (c *Client) ResolveGuild(ctx context.Context, input string) (id, name string, err error) {
	if guildIDPattern.MatchString(input) {
		return input, "", nil
	}

	code := inviteCode(input)
	if code == "" {
		return "", "", fmt.Errorf("invalid invite or guild ID %q", input)
	}

	var inv inviteResponse
	status, err := c.getJSON(ctx, "/invites/"+url.PathEscape(code), &inv, false)
	if err != nil {
		if status == http.StatusNotFound {
			return "", "", fmt.Errorf("invite %q not found", code)
		}
		return "", "", fmt.Errorf("resolving invite %q: %w", code, err)
	}
	if inv.Guild.ID == "" {
		return "", "", fmt.Errorf("invite %q is not a server invite", code)
	}

	return inv.Guild.ID, inv.Guild.Name, nil
}

func (c *Client) FetchGuildData(ctx context.Context, guildID, knownName string) (*GuildData, error) {
	var preview guildPreview
	status, err := c.getJSON(ctx, "/guilds/"+guildID+"/preview", &preview, true)
	if err == nil {
		return &GuildData{
			ID:       guildID,
			Name:     preview.Name,
			Emojis:   preview.Emojis,
			Stickers: preview.Stickers,
		}, nil
	}
	if status == http.StatusUnauthorized {
		return nil, errors.New("discord rejected the bot token (HTTP 401); check -token or DISCORD_TOKEN")
	}
	if status != http.StatusForbidden && status != http.StatusNotFound {
		return nil, fmt.Errorf("fetching guild preview: %w", err)
	}

	// Fallback for guilds the bot is a member of.
	var emojis []Emoji
	if _, err := c.getJSON(ctx, "/guilds/"+guildID+"/emojis", &emojis, true); err != nil {
		return nil, fmt.Errorf("guild preview unavailable and emoji listing failed (is the bot in the server?): %w", err)
	}

	var stickers []Sticker
	if _, err := c.getJSON(ctx, "/guilds/"+guildID+"/stickers", &stickers, true); err != nil {
		return nil, fmt.Errorf("guild preview unavailable and sticker listing failed (is the bot in the server?): %w", err)
	}

	name := knownName
	if name == "" {
		if fetched, nameErr := c.fetchGuildName(ctx, guildID); nameErr == nil {
			name = fetched
		}
	}

	return &GuildData{ID: guildID, Name: name, Emojis: emojis, Stickers: stickers}, nil
}

func (c *Client) fetchGuildName(ctx context.Context, guildID string) (string, error) {
	var guild struct {
		Name string `json:"name"`
	}
	if _, err := c.getJSON(ctx, "/guilds/"+guildID, &guild, true); err != nil {
		return "", err
	}
	return guild.Name, nil
}

func (c *Client) getJSON(ctx context.Context, apiPath string, dst any, auth bool) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+apiPath, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	if auth {
		req.Header.Set("Authorization", "Bot "+c.token)
	}

	resp, err := c.do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, fmt.Errorf("discord API %s: HTTP %d: %s", apiPath, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return resp.StatusCode, fmt.Errorf("decoding %s: %w", apiPath, err)
	}
	return resp.StatusCode, nil
}

func (c *Client) download(ctx context.Context, urls []string) (io.ReadCloser, string, error) {
	var lastErr error

	for _, rawURL := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := c.do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusOK {
			ext := strings.ToLower(path.Ext(req.URL.Path))
			if ext == "" {
				ext = ".bin"
			}
			return resp.Body, ext, nil
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		lastErr = fmt.Errorf("%s: HTTP %d: %s", rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnsupportedMediaType {
			continue
		}
		break
	}

	if lastErr == nil {
		lastErr = errors.New("no download candidates")
	}
	return nil, "", lastErr
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	const maxAttempts = 3

	for attempt := 1; ; attempt++ {
		resp, err := c.http.Do(req)
		if err != nil {
			if attempt >= maxAttempts || req.Context().Err() != nil {
				return nil, err
			}
			sleepContext(req.Context(), time.Duration(attempt)*time.Second)
			continue
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := retryAfter(resp)
			resp.Body.Close()
			if attempt >= maxAttempts {
				return nil, errors.New("rate limited by Discord")
			}
			sleepContext(req.Context(), wait)
		case resp.StatusCode >= 500:
			if attempt >= maxAttempts {
				return resp, nil
			}
			resp.Body.Close()
			sleepContext(req.Context(), time.Duration(attempt)*time.Second)
		default:
			return resp, nil
		}
	}
}

func retryAfter(resp *http.Response) time.Duration {
	if value := resp.Header.Get("Retry-After"); value != "" {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
			return time.Duration(seconds * float64(time.Second))
		}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.RetryAfter > 0 {
		return time.Duration(payload.RetryAfter * float64(time.Second))
	}

	return time.Second
}

func sleepContext(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func inviteCode(input string) string {
	input = strings.Trim(strings.TrimSpace(input), "/")
	if input == "" {
		return ""
	}

	if strings.Contains(input, "/") {
		if !strings.Contains(input, "://") {
			input = "https://" + input
		}
		u, err := url.Parse(input)
		if err != nil {
			return ""
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 0 {
			return ""
		}
		input = parts[len(parts)-1]
	}

	if !invitePattern.MatchString(input) {
		return ""
	}
	return input
}
