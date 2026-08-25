package connections

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	goftp "github.com/jlaffaye/ftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ValidateConnection calls the platform-specific validator and returns the
// resolved account identifier (username/email/org name). Returns ("", nil)
// for platforms with no validation or browser-session platforms.
func ValidateConnection(ctx context.Context, c *Connection) (accountID string, err error) {
	switch c.Platform {
	case "github":
		return validateGitHub(ctx, c)
	case "notion":
		return validateNotion(ctx, c)
	case "airtable":
		return validateAirtable(ctx, c)
	case "jira":
		return validateJira(ctx, c)
	case "linear":
		return validateLinear(ctx, c)
	case "stripe":
		return validateStripe(ctx, c)
	case "slack":
		return validateSlack(ctx, c)
	case "discord":
		return validateDiscord(ctx, c)
	case "bluesky":
		return validateBluesky(ctx, c)
	case "reddit":
		return validateReddit(ctx, c)
	case "mastodon":
		return validateMastodon(ctx, c)
	case "twilio":
		return validateTwilio(ctx, c)
	case "telegram":
		return validateTelegram(ctx, c)
	case "google_sheets", "google_drive", "gmail":
		return validateGoogle(ctx, c)
	case "youtube":
		return validateYouTube(ctx, c)
	case "outlook":
		if c.Method == MethodOAuth {
			return validateOutlookOAuth(ctx, c)
		}
		return validateOutlookAppPassword(ctx, c)
	case "hubspot":
		return validateHubSpot(ctx, c)
	case "salesforce":
		return validateSalesforce(ctx, c)
	case "devto":
		return validateDevTo(ctx, c)
	case "hashnode":
		return validateHashnode(ctx, c)
	case "producthunt":
		return validateProductHunt(ctx, c)
	case "ssh", "sftp":
		return validateSSH(ctx, c)
	case "ftp":
		return validateFTP(ctx, c)
	case "generic_api", "generic_basic":
		baseURL := getStr(c.Data, "base_url")
		if baseURL == "" {
			return "", fmt.Errorf("validate %s: missing base_url", c.Platform)
		}
		return baseURL, nil
	case "postgresql", "mysql", "mongodb", "redis":
		cs := getStr(c.Data, "connection_string")
		if cs == "" {
			return "", fmt.Errorf("validate %s: missing connection_string", c.Platform)
		}
		// The connection string embeds the DB password; never return it
		// verbatim — AccountID/Label survive Redact() and reach `connect
		// list --json` and GUI output. url.URL.Redacted() masks the
		// password while keeping the useful host/user/db identifier.
		if u, err := url.Parse(cs); err == nil && u.Host != "" {
			return u.Redacted(), nil
		}
		// Unparseable (e.g. key=value DSN) — a generic label rather than
		// risk leaking a password embedded somewhere in the raw string.
		return c.Platform + " database", nil
	default:
		return "", nil
	}
}

// validateSSH validates an SSH/SFTP connection by performing a live SSH
// handshake and authentication (no remote command is run) — without this,
// any garbage host/username/password/private_key gets saved as "connected"
// and only fails later when a workflow node tries to actually use it.
func validateSSH(ctx context.Context, c *Connection) (string, error) {
	host := getStr(c.Data, "host")
	username := getStr(c.Data, "username")
	if host == "" || username == "" {
		return "", fmt.Errorf("validateSSH: host and username are required")
	}

	port := 22
	if p := getStr(c.Data, "port"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	var authMethods []ssh.AuthMethod
	if privateKeyPEM := getStr(c.Data, "private_key"); privateKeyPEM != "" {
		var signer ssh.Signer
		var err error
		if passphrase := getStr(c.Data, "passphrase"); passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKeyPEM), []byte(passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(privateKeyPEM))
		}
		if err != nil {
			return "", fmt.Errorf("validateSSH: parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if password := getStr(c.Data, "password"); password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}
	if len(authMethods) == 0 {
		return "", fmt.Errorf("validateSSH: password or private_key is required")
	}

	// Mirrors internal/nodes/http/ssh.go: verify against a known_hosts file
	// when given, otherwise skip host key verification for this liveness check.
	hostKeyCallback := ssh.InsecureIgnoreHostKey() //nolint:gosec
	if knownHosts := getStr(c.Data, "known_hosts"); knownHosts != "" {
		cb, err := knownhosts.New(knownHosts)
		if err != nil {
			return "", fmt.Errorf("validateSSH: parse known_hosts: %w", err)
		}
		hostKeyCallback = cb
	}

	cfg := &ssh.ClientConfig{
		User:            username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("validateSSH: dial: %w", err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, cfg)
	if err != nil {
		netConn.Close()
		return "", fmt.Errorf("validateSSH: handshake: %w", err)
	}
	ssh.NewClient(sshConn, chans, reqs).Close()

	return username + "@" + host, nil
}

// validateFTP validates an FTP connection by performing a live dial and
// login, mirroring the check internal/nodes/http/ftp.go performs at run time.
func validateFTP(ctx context.Context, c *Connection) (string, error) {
	host := getStr(c.Data, "host")
	username := getStr(c.Data, "username")
	password := getStr(c.Data, "password")
	if host == "" || username == "" {
		return "", fmt.Errorf("validateFTP: host and username are required")
	}

	port := 21
	if p := getStr(c.Data, "port"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := goftp.Dial(addr, goftp.DialWithTimeout(15*time.Second), goftp.DialWithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("validateFTP: dial: %w", err)
	}
	defer conn.Quit()

	if err := conn.Login(username, password); err != nil {
		return "", fmt.Errorf("validateFTP: login: %w", err)
	}

	return username + "@" + host, nil
}

// validateOutlookAppPassword validates an Outlook/Hotmail app-password
// connection by performing a live IMAP LOGIN — without this, any garbage
// email/password gets saved as a "connected" credential that only fails
// later when an actual workflow node tries to use it. Note: Microsoft has
// deprecated Basic Auth for IMAP on outlook.com/hotmail.com accounts, so
// this will legitimately fail for those even with correct credentials —
// use OAuth instead for those accounts.
func validateOutlookAppPassword(ctx context.Context, c *Connection) (string, error) {
	email := getStr(c.Data, "email")
	password := getStr(c.Data, "app_password")
	if email == "" || password == "" {
		return "", fmt.Errorf("validateOutlookAppPassword: email and app_password are required")
	}

	const host = "outlook.office365.com"
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 15 * time.Second},
		Config:    &tls.Config{ServerName: host},
	}
	conn, err := dialer.DialContext(ctx, "tcp", host+":993")
	if err != nil {
		return "", fmt.Errorf("validateOutlookAppPassword: connect: %w", err)
	}
	tc := textproto.NewConn(conn)
	defer tc.Close()

	if _, err := tc.ReadLine(); err != nil {
		return "", fmt.Errorf("validateOutlookAppPassword: greeting: %w", err)
	}
	if _, err := fmt.Fprintf(conn, "A1 LOGIN %q %q\r\n", email, password); err != nil {
		return "", fmt.Errorf("validateOutlookAppPassword: login send: %w", err)
	}
	for {
		line, err := tc.ReadLine()
		if err != nil {
			return "", fmt.Errorf("validateOutlookAppPassword: login: %w", err)
		}
		if strings.HasPrefix(line, "A1 OK") {
			return email, nil
		}
		if strings.HasPrefix(line, "A1 NO") || strings.HasPrefix(line, "A1 BAD") {
			return "", fmt.Errorf("validateOutlookAppPassword: IMAP login rejected: %s", line)
		}
	}
}

// validateGitHub validates a GitHub connection using the token or access_token field.
func validateGitHub(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "token")
	if token == "" {
		token = getStr(c.Data, "access_token")
	}
	if token == "" {
		return "", fmt.Errorf("validateGitHub: missing token or access_token")
	}
	body, status, err := doGET(ctx, "https://api.github.com/user", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateGitHub: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateGitHub: unexpected status %d", status)
	}
	var resp struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("validateGitHub: parse response: %w", err)
	}
	if resp.Login == "" {
		return "", fmt.Errorf("validateGitHub: empty login in response")
	}
	return resp.Login, nil
}

// validateNotion validates a Notion connection using the token or access_token field.
func validateNotion(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "token")
	if token == "" {
		token = getStr(c.Data, "access_token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.notion.com/v1/users/me", nil)
	if err != nil {
		return "", fmt.Errorf("validateNotion: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", "2022-06-28")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateNotion: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateNotion: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateNotion: unexpected status %d", resp.StatusCode)
	}

	var r struct {
		Name string `json:"name"`
		Bot  struct {
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("validateNotion: parse response: %w", err)
	}

	if r.Bot.WorkspaceName != "" {
		return r.Bot.WorkspaceName, nil
	}
	if r.Name != "" {
		return r.Name, nil
	}
	return "", fmt.Errorf("validateNotion: could not resolve account name from response")
}

// validateAirtable validates an Airtable connection using the api_key or access_token field.
func validateAirtable(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "api_key")
	if token == "" {
		token = getStr(c.Data, "access_token")
	}
	body, status, err := doGET(ctx, "https://api.airtable.com/v0/meta/whoami", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateAirtable: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateAirtable: unexpected status %d", status)
	}
	var r struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	_ = json.Unmarshal(body, &r)
	if r.Email != "" {
		return r.Email, nil
	}
	return r.ID, nil
}

// validateJira validates a Jira connection using email, api_token, and domain fields.
func validateJira(ctx context.Context, c *Connection) (string, error) {
	email := getStr(c.Data, "email")
	apiToken := getStr(c.Data, "api_token")
	domain := getStr(c.Data, "domain")

	if email == "" {
		return "", fmt.Errorf("validateJira: missing email")
	}
	if apiToken == "" {
		return "", fmt.Errorf("validateJira: missing api_token")
	}
	if domain == "" {
		return "", fmt.Errorf("validateJira: missing domain")
	}

	url := fmt.Sprintf("https://%s/rest/api/3/myself", domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("validateJira: create request: %w", err)
	}
	req.SetBasicAuth(email, apiToken)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateJira: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateJira: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateJira: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		DisplayName string `json:"displayName"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("validateJira: parse response: %w", err)
	}
	return result.DisplayName, nil
}

// linearAuthHeader returns the Authorization header value for a Linear
// connection: personal API keys are sent as-is, OAuth access tokens need
// the standard "Bearer " prefix — sending either the wrong way
// authenticates neither. Returns "" if neither field is present.
// https://linear.app/developers/oauth-2-0-authentication
func linearAuthHeader(data map[string]interface{}) string {
	if apiKey := getStr(data, "api_key"); apiKey != "" {
		return apiKey
	}
	if token := getStr(data, "access_token"); token != "" {
		return "Bearer " + token
	}
	return ""
}

// validateLinear validates a Linear connection using the api_key or access_token field.
func validateLinear(ctx context.Context, c *Connection) (string, error) {
	authHeader := linearAuthHeader(c.Data)

	bodyStr := `{"query":"{ viewer { name email } }"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linear.app/graphql", strings.NewReader(bodyStr))
	if err != nil {
		return "", fmt.Errorf("validateLinear: create request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateLinear: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateLinear: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateLinear: unexpected status %d", resp.StatusCode)
	}

	var r struct {
		Data struct {
			Viewer struct {
				Email string `json:"email"`
				Name  string `json:"name"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("validateLinear: parse response: %w", err)
	}
	if r.Data.Viewer.Email != "" {
		return r.Data.Viewer.Email, nil
	}
	if r.Data.Viewer.Name != "" {
		return r.Data.Viewer.Name, nil
	}
	return "", fmt.Errorf("validateLinear: viewer has no email or name")
}

// validateStripe validates a Stripe connection using the secret_key field.
func validateStripe(ctx context.Context, c *Connection) (string, error) {
	key := getStr(c.Data, "secret_key")
	if key == "" {
		return "", fmt.Errorf("validateStripe: missing secret_key")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.stripe.com/v1/account", nil)
	if err != nil {
		return "", fmt.Errorf("validateStripe: create request: %w", err)
	}
	req.SetBasicAuth(key, "")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateStripe: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateStripe: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateStripe: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		BusinessProfile struct {
			Name string `json:"name"`
		} `json:"business_profile"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("validateStripe: parse response: %w", err)
	}
	if result.BusinessProfile.Name != "" {
		return result.BusinessProfile.Name, nil
	}
	return result.Email, nil
}

// validateSlack validates a Slack connection using the access_token field.
func validateSlack(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	body, status, err := doGET(ctx, "https://slack.com/api/auth.test", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateSlack: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateSlack: unexpected status %d", status)
	}

	var resp struct {
		OK    bool   `json:"ok"`
		Team  string `json:"team"`
		User  string `json:"user"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("validateSlack: parse response: %w", err)
	}
	if !resp.OK {
		return "", fmt.Errorf("validateSlack: api error: %s", resp.Error)
	}
	return fmt.Sprintf("%s / %s", resp.Team, resp.User), nil
}

// validateDiscord validates a Discord bot connection using the bot_token field.
func validateDiscord(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "bot_token")
	body, status, err := doGET(ctx, "https://discord.com/api/v10/users/@me", "Bot "+token)
	if err != nil {
		return "", fmt.Errorf("validateDiscord: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateDiscord: unexpected status %d", status)
	}

	var resp struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("validateDiscord: parse response: %w", err)
	}
	return resp.Username, nil
}

// validateBluesky validates a Bluesky connection by creating a session
// with the identifier/app_password fields — AT Protocol has no long-lived
// token to independently verify, so a successful session creation IS the check.
func validateBluesky(ctx context.Context, c *Connection) (string, error) {
	identifier := getStr(c.Data, "identifier")
	password := getStr(c.Data, "app_password")

	body, err := json.Marshal(map[string]string{"identifier": identifier, "password": password})
	if err != nil {
		return "", fmt.Errorf("validateBluesky: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://bsky.social/xrpc/com.atproto.server.createSession", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("validateBluesky: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateBluesky: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateBluesky: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateBluesky: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("validateBluesky: parse response: %w", err)
	}
	return result.Handle, nil
}

// redditUserAgent is Reddit's required custom User-Agent format:
// "platform:app_id:version (by /u/username)". Reddit aggressively
// rate-limits requests using default/generic User-Agent strings.
const redditUserAgent = "monoagent:workflow-node:1.0 (by /u/monoagent)"

// validateReddit validates a Reddit OAuth connection using the access_token field.
func validateReddit(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://oauth.reddit.com/api/v1/me", nil)
	if err != nil {
		return "", fmt.Errorf("validateReddit: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", redditUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateReddit: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateReddit: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateReddit: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("validateReddit: parse response: %w", err)
	}
	return result.Name, nil
}

// validateMastodon validates a Mastodon connection using instance_url and access_token.
func validateMastodon(ctx context.Context, c *Connection) (string, error) {
	instanceURL := strings.TrimSuffix(getStr(c.Data, "instance_url"), "/")
	token := getStr(c.Data, "access_token")
	if instanceURL == "" {
		return "", fmt.Errorf("validateMastodon: missing instance_url")
	}
	body, status, err := doGET(ctx, instanceURL+"/api/v1/accounts/verify_credentials", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateMastodon: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateMastodon: unexpected status %d", status)
	}

	var resp struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("validateMastodon: parse response: %w", err)
	}
	return resp.Username, nil
}

// validateTwilio validates a Twilio connection using account_sid and auth_token fields.
func validateTwilio(ctx context.Context, c *Connection) (string, error) {
	accountSID := getStr(c.Data, "account_sid")
	authToken := getStr(c.Data, "auth_token")

	if accountSID == "" || authToken == "" {
		return "", fmt.Errorf("validateTwilio: missing account_sid or auth_token")
	}

	url := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s.json", accountSID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("validateTwilio: create request: %w", err)
	}
	req.SetBasicAuth(accountSID, authToken)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateTwilio: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateTwilio: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateTwilio: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		FriendlyName string `json:"friendly_name"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("validateTwilio: parse response: %w", err)
	}
	return result.FriendlyName, nil
}

// validateTelegram validates a Telegram bot token using the getMe endpoint.
func validateTelegram(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "bot_token")
	if token == "" {
		return "", fmt.Errorf("validateTelegram: missing bot_token")
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token)
	body, status, err := doGET(ctx, url, "")
	if err != nil {
		return "", fmt.Errorf("validateTelegram: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateTelegram: invalid token (status %d)", status)
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("validateTelegram: parse response: %w", err)
	}
	if !resp.OK {
		return "", fmt.Errorf("validateTelegram: token rejected by Telegram")
	}
	if resp.Result.Username != "" {
		return "@" + resp.Result.Username, nil
	}
	return resp.Result.FirstName, nil
}

// getStr extracts a string value from a map, returning "" if missing or not a string.
func getStr(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	v, ok := data[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// doGET makes a GET request with the given Authorization header (empty = no header),
// sets Accept: application/json, and returns body bytes, HTTP status code, and any error.
func doGET(ctx context.Context, url, authHeader string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("doGET: create request: %w", err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("doGET: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("doGET: read body: %w", err)
	}
	return body, resp.StatusCode, nil
}

// validateGoogle validates a Google OAuth connection (Sheets, Drive, Gmail)
// by calling the Drive about endpoint (works with drive.readonly / spreadsheets scopes)
// or the Gmail profile endpoint depending on the platform.
func validateGoogle(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	if token == "" {
		return "", fmt.Errorf("validateGoogle: missing access_token")
	}

	// Try Gmail profile endpoint first for gmail platform
	if c.Platform == "gmail" {
		body, status, err := doGET(ctx, "https://gmail.googleapis.com/gmail/v1/users/me/profile", "Bearer "+token)
		if err != nil {
			return "", fmt.Errorf("validateGoogle: %w", err)
		}
		if status == 401 {
			return "", fmt.Errorf("validateGoogle: token expired or invalid (HTTP 401)")
		}
		if status == 200 {
			var r struct {
				EmailAddress string `json:"emailAddress"`
			}
			_ = json.Unmarshal(body, &r)
			if r.EmailAddress != "" {
				return r.EmailAddress, nil
			}
		}
	}

	// For Sheets/Drive (or Gmail fallback): use Drive about endpoint
	body, status, err := doGET(ctx, "https://www.googleapis.com/drive/v3/about?fields=user", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateGoogle: %w", err)
	}
	if status == 401 {
		return "", fmt.Errorf("validateGoogle: token expired or invalid (HTTP 401)")
	}
	if status != 200 {
		return "", fmt.Errorf("validateGoogle: unexpected status %d: %s", status, string(body))
	}
	var r struct {
		User struct {
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("validateGoogle: parse response: %w", err)
	}
	if r.User.EmailAddress != "" {
		return r.User.EmailAddress, nil
	}
	if r.User.DisplayName != "" {
		return r.User.DisplayName, nil
	}
	return "", nil
}

// validateYouTube validates a YouTube OAuth connection using the access_token field.
func validateYouTube(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	body, status, err := doGET(ctx, "https://www.googleapis.com/youtube/v3/channels?part=snippet&mine=true", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateYouTube: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateYouTube: unexpected status %d", status)
	}

	var resp struct {
		Items []struct {
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("validateYouTube: parse response: %w", err)
	}
	if len(resp.Items) == 0 {
		return "", fmt.Errorf("validateYouTube: no channel found for this account")
	}
	return resp.Items[0].Snippet.Title, nil
}

// validateOutlookOAuth validates an Outlook OAuth connection via Microsoft Graph.
func validateOutlookOAuth(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	if token == "" {
		return "", fmt.Errorf("validateOutlookOAuth: missing access_token")
	}
	// Resolves the real mailbox address (not just a liveness check) so
	// connect/refresh/test all keep AccountID/Label showing which account is
	// actually behind this connection — a profile name is a human label,
	// not a guarantee of which mailbox got authenticated. /me itself needs
	// a User.Read scope this app never requests (only Mail.ReadWrite/
	// Mail.Send/offline_access), so the address is read from the smallest
	// possible mail data instead: one field of at most one message.
	address, _, err := outlookWhoAmI(ctx, token)
	if err != nil {
		return "", fmt.Errorf("validateOutlookOAuth: %w", err)
	}
	return address, nil
}

// outlookWhoAmI resolves the mailbox owner's own address using the smallest
// possible read: one field ("from" or "toRecipients") of at most one
// message, no subject/body/content fetched.
func outlookWhoAmI(ctx context.Context, token string) (address, source string, err error) {
	// A Sent Items message's "from" is always the account owner.
	body, status, err := doGET(ctx, "https://graph.microsoft.com/v1.0/me/mailFolders/sentitems/messages?$top=1&$select=from", "Bearer "+token)
	if err == nil && status == 200 {
		if addr := firstEmailAddressField(body, "from"); addr != "" {
			return addr, "sentitems", nil
		}
	}
	// Fall back to Inbox: mail addressed to the owner names them in toRecipients.
	body, status, err = doGET(ctx, "https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages?$top=1&$select=toRecipients", "Bearer "+token)
	if err != nil {
		return "", "", err
	}
	if status != 200 {
		return "", "", fmt.Errorf("unexpected status %d: %s", status, string(body))
	}
	if addr := firstEmailAddressField(body, "toRecipients"); addr != "" {
		return addr, "inbox", nil
	}
	return "", "", fmt.Errorf("mailbox has no sent or received messages to resolve identity from")
}

// firstEmailAddressField extracts the address from the first message's given
// field in a raw Graph list response, which is either a single
// {"emailAddress":{...}} object ("from") or an array of them ("toRecipients").
func firstEmailAddressField(body []byte, field string) string {
	var parsed struct {
		Value []map[string]interface{} `json:"value"`
	}
	if json.Unmarshal(body, &parsed) != nil || len(parsed.Value) == 0 {
		return ""
	}
	msg := parsed.Value[0]
	switch field {
	case "from":
		fromObj, _ := msg["from"].(map[string]interface{})
		ea, _ := fromObj["emailAddress"].(map[string]interface{})
		addr, _ := ea["address"].(string)
		return addr
	case "toRecipients":
		recipients, _ := msg["toRecipients"].([]interface{})
		if len(recipients) == 0 {
			return ""
		}
		rm, _ := recipients[0].(map[string]interface{})
		ea, _ := rm["emailAddress"].(map[string]interface{})
		addr, _ := ea["address"].(string)
		return addr
	}
	return ""
}

// validateHubSpot validates a HubSpot OAuth connection.
func validateHubSpot(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	if token == "" {
		return "", fmt.Errorf("validateHubSpot: missing access_token")
	}
	body, status, err := doGET(ctx, "https://api.hubapi.com/oauth/v1/access-tokens/"+token, "")
	if err != nil {
		return "", fmt.Errorf("validateHubSpot: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateHubSpot: unexpected status %d", status)
	}
	var r struct {
		User  string `json:"user"`
		HubID int    `json:"hub_id"`
	}
	_ = json.Unmarshal(body, &r)
	if r.User != "" {
		return r.User, nil
	}
	return fmt.Sprintf("hub-%d", r.HubID), nil
}

// validateSalesforce validates a Salesforce OAuth connection.
func validateSalesforce(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	instanceURL := getStr(c.Data, "instance_url")
	if token == "" {
		return "", fmt.Errorf("validateSalesforce: missing access_token")
	}
	if instanceURL == "" {
		instanceURL = "https://login.salesforce.com"
	}
	body, status, err := doGET(ctx, instanceURL+"/services/oauth2/userinfo", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateSalesforce: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateSalesforce: unexpected status %d", status)
	}
	var r struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	_ = json.Unmarshal(body, &r)
	if r.Email != "" {
		return r.Email, nil
	}
	return r.Name, nil
}

// validateDevTo checks a Dev.to API key by fetching the authenticated user.
func validateDevTo(ctx context.Context, c *Connection) (string, error) {
	apiKey := getStr(c.Data, "api_key")
	if apiKey == "" {
		return "", fmt.Errorf("validateDevTo: missing api_key")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://dev.to/api/users/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("api-key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateDevTo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateDevTo: API returned %d", resp.StatusCode)
	}
	var r struct {
		Username string `json:"username"`
	}
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &r)
	if r.Username == "" {
		return "", fmt.Errorf("validateDevTo: could not resolve username")
	}
	return r.Username, nil
}

// validateHashnode checks a Hashnode personal access token via the GraphQL API.
func validateHashnode(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "token")
	if token == "" {
		return "", fmt.Errorf("validateHashnode: missing token")
	}
	payload := []byte(`{"query":"{ me { username } }"}`)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://gql.hashnode.com", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	// Don't follow redirects: since 2026-05-13 Hashnode 301s every GraphQL
	// request — valid token or not — to an HTML announcement page when the
	// publication isn't on a Pro plan. The default client would silently
	// follow it, fail to parse HTML as JSON, and misreport this as a bad
	// token via the "could not resolve username" case below.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateHashnode: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return "", fmt.Errorf("validateHashnode: Hashnode redirected this request to %s instead of returning data — the publication likely needs a Pro plan for API access (see https://hashnode.com/changelog/2026-05-13-graphql-api-paid-access); the token was never actually checked", resp.Header.Get("Location"))
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateHashnode: API returned %d", resp.StatusCode)
	}
	var r struct {
		Data struct {
			Me struct {
				Username string `json:"username"`
			} `json:"me"`
		} `json:"data"`
	}
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &r)
	if r.Data.Me.Username == "" {
		return "", fmt.Errorf("validateHashnode: could not resolve username")
	}
	return r.Data.Me.Username, nil
}

// validateProductHunt checks a Product Hunt developer token via the GraphQL API.
func validateProductHunt(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	if token == "" {
		return "", fmt.Errorf("validateProductHunt: missing access_token")
	}
	payload := []byte(`{"query":"{ viewer { user { name } } }"}`)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.producthunt.com/v2/api/graphql", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateProductHunt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateProductHunt: API returned %d", resp.StatusCode)
	}
	var r struct {
		Data struct {
			Viewer struct {
				User struct {
					Name string `json:"name"`
				} `json:"user"`
			} `json:"viewer"`
		} `json:"data"`
	}
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &r)
	if r.Data.Viewer.User.Name == "" {
		return "", fmt.Errorf("validateProductHunt: could not resolve viewer name")
	}
	return r.Data.Viewer.User.Name, nil
}
