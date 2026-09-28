package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds Blink API runtime settings. Production fails closed on secrets.
type Config struct {
	Port               string
	DatabaseURL        string
	CORSOrigins        []string
	AgentRuntimeURL    string
	AgentRuntimeToken  string
	AgentLambdaName    string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSRegion          string
	S3BucketName       string
	S3PublicBaseURL    string
	AutomationSDLCPath string
	AutomationSDLCGit  string
	CanonicalPython    string
	CanonicalTimeout   time.Duration

	JiraClientID     string
	JiraClientSecret string
	JiraRedirectURI  string
	JiraScopes       string

	GitHubClientID     string
	GitHubClientSecret string
	GitHubRedirectURI  string
	GitHubScopes       string

	FigmaClientID     string
	FigmaClientSecret string
	FigmaRedirectURI  string
	FigmaScopes       string
	StitchAPIKey      string

	IntegrationSecretKey string

	SMTPHost      string
	SMTPPort      int
	SMTPUsername  string
	SMTPPassword  string
	SMTPFrom      string
	SMTPStartTLS  bool
	SMTPOutboxDir string
	SMTPEnabled   bool

	GmailClientID     string
	GmailClientSecret string
	GmailRefreshToken string
	GmailFrom         string
	GmailRedirectURI  string

	LoginAllowedDomain string
	OTPttl             time.Duration
	OTPResendCooldown  time.Duration
	SessionTTL         time.Duration
	OTPMaxAttempts     int
	OTPReveal          bool
	LoginGate          string

	DemoUserEmail string
	ProdMode      bool
}

func Load() (Config, error) {
	// Prefer cwd .env; also try module-root style paths when launched via `go run`.
	_ = godotenv.Load()
	_ = godotenv.Load(".env")
	if _, err := os.Stat("cmd/server"); err == nil {
		// already at module root
	} else {
		_ = godotenv.Load("../../.env")
	}
	c := Config{
		Port:               env("PORT", "8090"),
		DatabaseURL:        firstNonEmpty(os.Getenv("DATABASE_URL"), os.Getenv("SPRING_DATASOURCE_URL"), "postgres://blink:blink@localhost:5432/blink?sslmode=disable"),
		CORSOrigins:        splitCSV(env("BLINK_CORS_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173")),
		AgentRuntimeURL:    env("BLINK_AGENT_RUNTIME_URL", "https://z5i3yybrx1.execute-api.us-west-2.amazonaws.com"),
		AgentRuntimeToken:  os.Getenv("BLINK_AGENT_RUNTIME_TOKEN"),
		AgentLambdaName:    env("BLINK_AGENT_RUNTIME_LAMBDA_FUNCTION", "blink-agent-runtime"),
		AWSAccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		AWSRegion:          env("AWS_REGION", "us-west-2"),
		S3BucketName:       os.Getenv("S3_BUCKET_NAME"),
		S3PublicBaseURL:    os.Getenv("S3_PUBLIC_BASE_URL"),
		AutomationSDLCPath: env("BLINK_AUTOMATION_SDLC_PATH", "../automation_sdlc"),
		AutomationSDLCGit:  env("BLINK_AUTOMATION_SDLC_GIT_URL", "https://github.com/AtulTalentServ/automation_sdlc.git"),
		CanonicalPython:    env("BLINK_CANONICAL_SETUP_PYTHON", "python3"),
		CanonicalTimeout:   durationEnv("BLINK_CANONICAL_SETUP_TIMEOUT", 90*time.Second),

		JiraClientID:     os.Getenv("BLINK_JIRA_CLIENT_ID"),
		JiraClientSecret: os.Getenv("BLINK_JIRA_CLIENT_SECRET"),
		JiraRedirectURI:  os.Getenv("BLINK_JIRA_REDIRECT_URI"),
		JiraScopes:       env("BLINK_JIRA_SCOPES", "read:jira-work write:jira-work read:jira-user read:me offline_access"),

		GitHubClientID:     os.Getenv("BLINK_GITHUB_CLIENT_ID"),
		GitHubClientSecret: os.Getenv("BLINK_GITHUB_CLIENT_SECRET"),
		GitHubRedirectURI:  os.Getenv("BLINK_GITHUB_REDIRECT_URI"),
		GitHubScopes:       env("BLINK_GITHUB_SCOPES", "repo read:org user:email"),

		FigmaClientID:     os.Getenv("BLINK_FIGMA_CLIENT_ID"),
		FigmaClientSecret: os.Getenv("BLINK_FIGMA_CLIENT_SECRET"),
		FigmaRedirectURI:  os.Getenv("BLINK_FIGMA_REDIRECT_URI"),
		FigmaScopes:       env("BLINK_FIGMA_SCOPES", "current_user:read,file_content:read,file_metadata:read,webhooks:write"),
		StitchAPIKey:      os.Getenv("STITCH_API_KEY"),

		IntegrationSecretKey: os.Getenv("BLINK_INTEGRATION_SECRET_KEY"),

		SMTPHost:      os.Getenv("BLINK_SMTP_HOST"),
		SMTPPort:      intEnv("BLINK_SMTP_PORT", 587),
		SMTPUsername:  os.Getenv("BLINK_SMTP_USERNAME"),
		SMTPPassword:  os.Getenv("BLINK_SMTP_PASSWORD"),
		SMTPFrom:      env("BLINK_SMTP_FROM", "blink@localhost"),
		SMTPStartTLS:  boolEnv("BLINK_SMTP_START_TLS", true),
		SMTPOutboxDir: env("BLINK_SMTP_OUTBOX_DIR", ".blink-outbox"),
		SMTPEnabled:   boolEnv("BLINK_SMTP_ENABLED", false),

		GmailClientID:     os.Getenv("BLINK_GMAIL_CLIENT_ID"),
		GmailClientSecret: os.Getenv("BLINK_GMAIL_CLIENT_SECRET"),
		GmailRefreshToken: os.Getenv("BLINK_GMAIL_REFRESH_TOKEN"),
		GmailFrom:         os.Getenv("BLINK_GMAIL_FROM"),
		GmailRedirectURI:  os.Getenv("BLINK_GMAIL_REDIRECT_URI"),

		LoginAllowedDomain: env("BLINK_LOGIN_ALLOWED_DOMAIN", "talentserv.co.in"),
		OTPttl:             durationEnv("BLINK_OTP_TTL", 5*time.Minute),
		OTPResendCooldown:  durationEnv("BLINK_OTP_RESEND_COOLDOWN", 45*time.Second),
		SessionTTL:         durationEnv("BLINK_SESSION_TTL", 12*time.Hour),
		OTPMaxAttempts:     intEnv("BLINK_OTP_MAX_ATTEMPTS", 5),
		OTPReveal:          false,
		LoginGate:          os.Getenv("BLINK_LOGIN_GATE"),

		DemoUserEmail: env("BLINK_DEMO_USER_EMAIL", "blink.system@talentserv.com"),
		ProdMode:      boolEnv("BLINK_PROD", false) || strings.EqualFold(os.Getenv("RENDER"), "true"),
	}
	c.OTPReveal = otpRevealFromEnv(c.LoginGate)
	if c.ProdMode {
		if strings.TrimSpace(c.AgentRuntimeToken) == "" {
			return c, errSecret("BLINK_AGENT_RUNTIME_TOKEN is required in production")
		}
		if c.AgentRuntimeToken == "blink-groom-2026" {
			fmt.Fprintln(os.Stderr, "warning: BLINK_AGENT_RUNTIME_TOKEN is still the legacy default; rotate it in Render")
		}
		if strings.TrimSpace(c.IntegrationSecretKey) == "" {
			fmt.Fprintln(os.Stderr, "warning: BLINK_INTEGRATION_SECRET_KEY unset in production; encrypted integrations will use an ephemeral default")
		}
	}
	return c, nil
}

type secretError string

func (e secretError) Error() string { return string(e) }
func errSecret(msg string) error    { return secretError(msg) }

// GmailConfigured is true when the Gmail API mailbox can send OTPs over HTTPS.
func (c Config) GmailConfigured() bool {
	return strings.TrimSpace(c.GmailClientID) != "" &&
		strings.TrimSpace(c.GmailClientSecret) != "" &&
		strings.TrimSpace(c.GmailRefreshToken) != ""
}

// LocalMail shows the OTP on screen and skips SMTP while a login gate is set.
// Gmail OAuth or BLINK_SMTP_ENABLED=true sends real email instead.
func (c Config) LocalMail() bool {
	if c.SMTPEnabled || c.GmailConfigured() {
		return false
	}
	return strings.TrimSpace(c.LoginGate) != ""
}

func otpRevealFromEnv(gate string) bool {
	raw := strings.TrimSpace(os.Getenv("BLINK_OTP_REVEAL"))
	if raw == "" {
		return strings.TrimSpace(gate) != ""
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intEnv(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func boolEnv(k string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(k)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

func durationEnv(k string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
