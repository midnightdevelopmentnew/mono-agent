package connections

// AuthMethod represents the authentication method for a platform.
type AuthMethod string

const (
	MethodOAuth   AuthMethod = "oauth"
	MethodAPIKey  AuthMethod = "apikey"
	MethodBrowser AuthMethod = "browser"
	MethodConnStr AuthMethod = "connstring"
	MethodAppPass AuthMethod = "apppassword"
)

// CredentialField describes a single credential input field.
type CredentialField struct {
	Key      string
	Label    string
	Secret   bool
	Required bool
	HelpURL  string
	HelpText string
}

// OAuthConfig holds OAuth 2.0 endpoint configuration.
type OAuthConfig struct {
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
	CallbackPort int
	ExtraParams  map[string]string // Additional auth URL query parameters (e.g., access_type=offline)
}

// PlatformDef defines a platform's connection capabilities.
type PlatformDef struct {
	ID         string
	Name       string
	Category   string
	ConnectVia string
	Methods    []AuthMethod
	Fields     map[AuthMethod][]CredentialField
	OAuth      *OAuthConfig
	IconEmoji  string
}

// Registry is the map of all supported platforms keyed by platform ID.
var Registry = map[string]PlatformDef{

	// ─── Social ────────────────────────────────────────────────────────────────

	"instagram": {
		ID:         "instagram",
		Name:       "Instagram",
		Category:   "social",
		ConnectVia: "UI",
		Methods:    []AuthMethod{MethodBrowser},
		Fields:     map[AuthMethod][]CredentialField{},
		IconEmoji:  "📸",
	},
	"linkedin": {
		ID:         "linkedin",
		Name:       "LinkedIn",
		Category:   "social",
		ConnectVia: "UI",
		Methods:    []AuthMethod{MethodBrowser},
		Fields:     map[AuthMethod][]CredentialField{},
		IconEmoji:  "💼",
	},
	"x": {
		ID:         "x",
		Name:       "X (Twitter)",
		Category:   "social",
		ConnectVia: "UI",
		Methods:    []AuthMethod{MethodBrowser},
		Fields:     map[AuthMethod][]CredentialField{},
		IconEmoji:  "🐦",
	},
	"tiktok": {
		ID:         "tiktok",
		Name:       "TikTok",
		Category:   "social",
		ConnectVia: "UI",
		Methods:    []AuthMethod{MethodBrowser},
		Fields:     map[AuthMethod][]CredentialField{},
		IconEmoji:  "🎵",
	},
	"reddit": {
		ID:         "reddit",
		Name:       "Reddit",
		Category:   "social",
		ConnectVia: "API",
		// Register Reddit's OAuth app as "installed app" type (no client
		// secret) — this fits the existing generic PKCE flow in oauth.go
		// with no code changes; Reddit's "web app" type requires HTTP Basic
		// auth on token exchange, which the generic flow doesn't do.
		Methods: []AuthMethod{MethodOAuth},
		Fields:  map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://www.reddit.com/api/v1/authorize",
			TokenURL:     "https://www.reddit.com/api/v1/access_token",
			Scopes:       []string{"submit", "read", "identity"},
			CallbackPort: 9876,
			ExtraParams:  map[string]string{"duration": "permanent"},
		},
		IconEmoji: "👽",
	},
	"mastodon": {
		ID:         "mastodon",
		Name:       "Mastodon",
		Category:   "social",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{Key: "instance_url", Label: "Instance URL (e.g. https://fosstodon.org)", Secret: false, Required: true},
				{Key: "access_token", Label: "Access Token", Secret: true, Required: true, HelpURL: "https://docs.joinmastodon.org/client/token/"},
			},
		},
		IconEmoji: "🐘",
	},
	"gemini": {
		ID:         "gemini",
		Name:       "Gemini Crawler",
		Category:   "service",
		ConnectVia: "UI",
		Methods:    []AuthMethod{MethodBrowser},
		Fields:     map[AuthMethod][]CredentialField{},
		IconEmoji:  "✨",
	},
	"telegram": {
		ID:         "telegram",
		Name:       "Telegram",
		Category:   "social",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "bot_token",
					Label:    "Bot Token",
					Secret:   true,
					Required: true,
					HelpURL:  "https://core.telegram.org/bots#creating-a-new-bot",
				},
			},
		},
		IconEmoji: "✈️",
	},

	// ─── Services ──────────────────────────────────────────────────────────────

	"github": {
		ID:         "github",
		Name:       "GitHub",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "token",
					Label:    "Personal Access Token",
					Secret:   true,
					Required: true,
					HelpURL:  "https://github.com/settings/tokens/new",
				},
			},
		},
		OAuth: &OAuthConfig{
			AuthURL:      "https://github.com/login/oauth/authorize",
			TokenURL:     "https://github.com/login/oauth/access_token",
			Scopes:       []string{"repo", "read:user", "user:email"},
			CallbackPort: 9876,
		},
		IconEmoji: "🐙",
	},
	"notion": {
		ID:         "notion",
		Name:       "Notion",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "token",
					Label:    "Integration Token",
					Secret:   true,
					Required: true,
					HelpURL:  "https://www.notion.so/my-integrations",
				},
			},
		},
		OAuth: &OAuthConfig{
			AuthURL:      "https://api.notion.com/v1/oauth/authorize",
			TokenURL:     "https://api.notion.com/v1/oauth/token",
			CallbackPort: 9876,
		},
		IconEmoji: "📝",
	},
	"airtable": {
		ID:         "airtable",
		Name:       "Airtable",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "api_key",
					Label:    "API Key",
					Secret:   true,
					Required: true,
					HelpURL:  "https://airtable.com/create/tokens",
				},
			},
		},
		OAuth: &OAuthConfig{
			AuthURL:      "https://airtable.com/oauth2/v1/authorize",
			TokenURL:     "https://airtable.com/oauth2/v1/token",
			Scopes:       []string{"data.records:read", "data.records:write", "schema.bases:read"},
			CallbackPort: 9876,
		},
		IconEmoji: "📊",
	},
	"jira": {
		ID:         "jira",
		Name:       "Jira",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{Key: "email", Label: "Email", Secret: false, Required: true},
				{Key: "api_token", Label: "API Token", Secret: true, Required: true},
				{Key: "domain", Label: "Jira Domain", Secret: false, Required: true},
			},
		},
		OAuth: &OAuthConfig{
			AuthURL:      "https://auth.atlassian.com/authorize",
			TokenURL:     "https://auth.atlassian.com/oauth/token",
			CallbackPort: 9876,
		},
		IconEmoji: "🎯",
	},
	"linear": {
		ID:         "linear",
		Name:       "Linear",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "api_key",
					Label:    "API Key",
					Secret:   true,
					Required: true,
					HelpURL:  "https://linear.app/settings/api",
				},
			},
		},
		OAuth: &OAuthConfig{
			AuthURL:      "https://linear.app/oauth/authorize",
			TokenURL:     "https://api.linear.app/oauth/token",
			Scopes:       []string{"read", "write"},
			CallbackPort: 9876,
		},
		IconEmoji: "📐",
	},
	"asana": {
		ID:         "asana",
		Name:       "Asana",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "access_token",
					Label:    "Personal Access Token",
					Secret:   true,
					Required: true,
					HelpURL:  "https://app.asana.com/0/my-apps",
				},
			},
		},
		OAuth: &OAuthConfig{
			AuthURL:      "https://app.asana.com/-/oauth_authorize",
			TokenURL:     "https://app.asana.com/-/oauth_token",
			Scopes:       []string{"default"},
			CallbackPort: 9876,
		},
		IconEmoji: "✅",
	},
	"stripe": {
		ID:         "stripe",
		Name:       "Stripe",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "secret_key",
					Label:    "Secret Key",
					Secret:   true,
					Required: true,
					HelpURL:  "https://dashboard.stripe.com/apikeys",
				},
			},
		},
		IconEmoji: "💳",
	},
	"shopify": {
		ID:         "shopify",
		Name:       "Shopify",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{Key: "shop_domain", Label: "Shop Domain", Secret: false, Required: true},
				{Key: "access_token", Label: "Access Token", Secret: true, Required: true},
			},
		},
		OAuth: &OAuthConfig{
			AuthURL:      "https://{shop}/admin/oauth/authorize",
			TokenURL:     "https://{shop}/admin/oauth/access_token",
			CallbackPort: 9876,
		},
		IconEmoji: "🛍️",
	},
	"salesforce": {
		ID:         "salesforce",
		Name:       "Salesforce",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth},
		Fields:     map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://login.salesforce.com/services/oauth2/authorize",
			TokenURL:     "https://login.salesforce.com/services/oauth2/token",
			Scopes:       []string{"api", "refresh_token"},
			CallbackPort: 9876,
		},
		IconEmoji: "☁️",
	},
	"hubspot": {
		ID:         "hubspot",
		Name:       "HubSpot",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth, MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "access_token",
					Label:    "Private App Access Token",
					Secret:   true,
					Required: true,
					HelpURL:  "https://app.hubspot.com/private-apps",
				},
			},
		},
		OAuth: &OAuthConfig{
			AuthURL:      "https://app.hubspot.com/oauth/authorize",
			TokenURL:     "https://api.hubapi.com/oauth/v1/token",
			CallbackPort: 9876,
		},
		IconEmoji: "🧡",
	},
	"google_sheets": {
		ID:         "google_sheets",
		Name:       "Google Sheets",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth},
		Fields:     map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			Scopes:       []string{"https://www.googleapis.com/auth/spreadsheets", "https://www.googleapis.com/auth/drive.readonly"},
			CallbackPort: 9876,
			ExtraParams:  map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		IconEmoji: "📗",
	},
	"gmail": {
		ID:         "gmail",
		Name:       "Gmail",
		Category:   "service",
		ConnectVia: "API",
		// App Password is intentionally not offered: internal/nodes/service/gmail.go
		// calls the Gmail REST API (gmail.googleapis.com), which is OAuth-only — an
		// app password can't authenticate against it (that only works for raw
		// SMTP/IMAP, which this node doesn't use).
		Methods: []AuthMethod{MethodOAuth},
		Fields:  map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			Scopes:       []string{"https://www.googleapis.com/auth/gmail.modify"},
			CallbackPort: 9876,
			ExtraParams:  map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		IconEmoji: "📧",
	},
	"google_drive": {
		ID:         "google_drive",
		Name:       "Google Drive",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth},
		Fields:     map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			Scopes:       []string{"https://www.googleapis.com/auth/drive"},
			CallbackPort: 9876,
			ExtraParams:  map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		IconEmoji: "📁",
	},
	"openrouter": {
		ID:         "openrouter",
		Name:       "OpenRouter",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "api_key",
					Label:    "API Key",
					Secret:   true,
					Required: true,
					HelpText: "Your OpenRouter API key. Find it at openrouter.ai/keys.",
				},
			},
		},
		IconEmoji: "🤖",
	},
	"huggingface": {
		ID:         "huggingface",
		Name:       "Hugging Face",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "api_key",
					Label:    "API Key",
					Secret:   true,
					Required: true,
					HelpText: "Your Hugging Face access token. Find it at huggingface.co/settings/tokens.",
				},
			},
		},
		IconEmoji: "🤗",
	},

	// ─── Communication ─────────────────────────────────────────────────────────

	"slack": {
		ID:         "slack",
		Name:       "Slack",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth},
		Fields:     map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://slack.com/oauth/v2/authorize",
			TokenURL:     "https://slack.com/api/oauth.v2.access",
			Scopes:       []string{"channels:read", "chat:write", "users:read"},
			CallbackPort: 9876,
		},
		IconEmoji: "💬",
	},
	"discord": {
		ID:         "discord",
		Name:       "Discord",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "bot_token",
					Label:    "Bot Token",
					Secret:   true,
					Required: true,
					HelpURL:  "https://discord.com/developers/applications",
				},
			},
		},
		IconEmoji: "🎮",
	},
	"twilio": {
		ID:         "twilio",
		Name:       "Twilio",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{
					Key:      "account_sid",
					Label:    "Account SID",
					Secret:   false,
					Required: true,
					HelpURL:  "https://console.twilio.com/",
				},
				{Key: "auth_token", Label: "Auth Token", Secret: true, Required: true, HelpURL: "https://console.twilio.com/"},
				{Key: "from_number", Label: "From Number", Secret: false, Required: true, HelpURL: "https://console.twilio.com/"},
			},
		},
		IconEmoji: "📞",
	},
	"whatsapp": {
		ID:         "whatsapp",
		Name:       "WhatsApp",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{Key: "account_sid", Label: "Account SID", Secret: false, Required: true},
				{Key: "auth_token", Label: "Auth Token", Secret: true, Required: true},
				{Key: "from_number", Label: "From Number", Secret: false, Required: true},
			},
		},
		IconEmoji: "📱",
	},
	"smtp": {
		ID:         "smtp",
		Name:       "SMTP / Email",
		Category:   "communication",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAppPass},
		Fields: map[AuthMethod][]CredentialField{
			MethodAppPass: {
				{Key: "email", Label: "Email", Secret: false, Required: true},
				{Key: "password", Label: "Password", Secret: true, Required: true},
				{Key: "smtp_host", Label: "SMTP Host", Secret: false, Required: true},
				{Key: "smtp_port", Label: "SMTP Port", Secret: false, Required: true},
				{Key: "imap_host", Label: "IMAP Host", Secret: false, Required: false},
				{Key: "imap_port", Label: "IMAP Port", Secret: false, Required: false},
			},
		},
		IconEmoji: "✉️",
	},
	"outlook": {
		ID:         "outlook",
		Name:       "Outlook / Hotmail",
		Category:   "communication",
		ConnectVia: "API",
		// MethodAppPass drives comm.outlook_send/outlook_read (raw IMAP/SMTP) —
		// Microsoft has deprecated Basic Auth for these on outlook.com/hotmail.com
		// accounts, so this method largely no longer works there; it's kept for
		// any IMAP/SMTP server that still accepts it.
		// MethodOAuth drives service.outlook_mail (Microsoft Graph REST API),
		// which needs no XOAUTH2/IMAP support — it's the working path today.
		Methods: []AuthMethod{MethodOAuth, MethodAppPass},
		Fields: map[AuthMethod][]CredentialField{
			MethodAppPass: {
				{
					Key:      "email",
					Label:    "Email Address",
					Secret:   false,
					Required: true,
					HelpText: "Your Outlook or Hotmail address (e.g. user@outlook.com)",
				},
				{
					Key:      "app_password",
					Label:    "App Password",
					Secret:   true,
					Required: true,
					HelpURL:  "https://account.microsoft.com/security",
					HelpText: "Generate an app password in your Microsoft account security settings",
				},
			},
		},
		OAuth: &OAuthConfig{
			// /common/: supports both personal (outlook.com/hotmail.com) and
			// work/school (Microsoft 365) accounts. Requires the Azure app's
			// "Supported account types" to be set to "Accounts in any
			// organizational directory and personal Microsoft accounts" —
			// Microsoft rejects /common/ with a "userAudience" invalid_request
			// if the app is still registered as "Personal accounts only".
			AuthURL:      "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			Scopes:       []string{"https://graph.microsoft.com/Mail.ReadWrite", "https://graph.microsoft.com/Mail.Send", "offline_access"},
			CallbackPort: 9876,
		},
		IconEmoji: "📨",
	},

	// ─── Databases ─────────────────────────────────────────────────────────────

	"postgresql": {
		ID:         "postgresql",
		Name:       "PostgreSQL",
		Category:   "database",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodConnStr},
		Fields: map[AuthMethod][]CredentialField{
			MethodConnStr: {
				{
					Key:      "connection_string",
					Label:    "Connection String",
					Secret:   true,
					Required: true,
					HelpText: "e.g. postgres://user:password@localhost:5432/dbname",
				},
			},
		},
		IconEmoji: "🐘",
	},
	"mysql": {
		ID:         "mysql",
		Name:       "MySQL",
		Category:   "database",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodConnStr},
		Fields: map[AuthMethod][]CredentialField{
			MethodConnStr: {
				{
					Key:      "connection_string",
					Label:    "Connection String",
					Secret:   true,
					Required: true,
					HelpText: "e.g. user:password@tcp(localhost:3306)/dbname",
				},
			},
		},
		IconEmoji: "🐬",
	},
	"mongodb": {
		ID:         "mongodb",
		Name:       "MongoDB",
		Category:   "database",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodConnStr},
		Fields: map[AuthMethod][]CredentialField{
			MethodConnStr: {
				{
					Key:      "connection_string",
					Label:    "Connection String",
					Secret:   true,
					Required: true,
					HelpText: "e.g. mongodb://user:password@localhost:27017/dbname",
				},
			},
		},
		IconEmoji: "🍃",
	},
	"redis": {
		ID:         "redis",
		Name:       "Redis",
		Category:   "database",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodConnStr},
		Fields: map[AuthMethod][]CredentialField{
			MethodConnStr: {
				{
					Key:      "connection_string",
					Label:    "Connection String",
					Secret:   true,
					Required: true,
					HelpText: "e.g. redis://:password@localhost:6379/0",
				},
			},
		},
		IconEmoji: "🔴",
	},
}

// Get returns the PlatformDef for the given ID and a boolean indicating existence.
func Get(id string) (PlatformDef, bool) {
	p, ok := Registry[id]
	return p, ok
}

// All returns all platform definitions as a slice.
func All() []PlatformDef {
	out := make([]PlatformDef, 0, len(Registry))
	for _, p := range Registry {
		out = append(out, p)
	}
	return out
}

// ByCategory returns all platforms in the given category.
func ByCategory(category string) []PlatformDef {
	var out []PlatformDef
	for _, p := range Registry {
		if p.Category == category {
			out = append(out, p)
		}
	}
	return out
}

// ByConnectVia returns all platforms with the given ConnectVia value.
func ByConnectVia(via string) []PlatformDef {
	var out []PlatformDef
	for _, p := range Registry {
		if p.ConnectVia == via {
			out = append(out, p)
		}
	}
	return out
}
