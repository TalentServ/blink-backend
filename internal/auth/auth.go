package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nisha-ts-40599/blink-backend/internal/config"
)

var (
	ErrUnauthorized = errors.New("sign in to continue")
	ErrForbidden    = errors.New("forbidden")
	ErrBadRequest   = errors.New("bad request")
)

type cachedSession struct {
	sess Session
	until time.Time
}

type Service struct {
	pool     *pgxpool.Pool
	cfg      config.Config
	mail     Mailer
	sessions sync.Map // tokenHash -> cachedSession
}

type Mailer interface {
	SendOTP(ctx context.Context, to, otp string) error
}

type Session struct {
	Email     string
	ExpiresAt time.Time
}

func New(pool *pgxpool.Pool, cfg config.Config, mail Mailer) *Service {
	return &Service{pool: pool, cfg: cfg, mail: mail}
}

func (s *Service) Config() map[string]any {
	return map[string]any{
		"allowedDomain": s.cfg.LoginAllowedDomain,
		"gateRequired":  strings.TrimSpace(s.cfg.LoginGate) != "",
		"otpReveal":     s.cfg.LocalMail(),
	}
}

type OTPRequest struct {
	Email      string `json:"email"`
	AccessCode string `json:"accessCode"`
}

type OTPRequestResponse struct {
	Message            string  `json:"message"`
	DeliveryMode       string  `json:"deliveryMode"`
	ExpiresInSeconds   int     `json:"expiresInSeconds"`
	ResendAfterSeconds int     `json:"resendAfterSeconds"`
	OTP                *string `json:"otp,omitempty"`
}

func (s *Service) RequestOTP(ctx context.Context, req OTPRequest) (*OTPRequestResponse, error) {
	email, err := s.normalizeAllowedEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if err := s.checkGate(req.AccessCode); err != nil {
		return nil, err
	}
	// Cold Neon often exceeds 12s; keep OTP ahead of other hung traffic.
	dbCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var lastSent time.Time
	_ = s.pool.QueryRow(dbCtx, `SELECT last_sent_at FROM blink_otp_challenge WHERE email=$1`, email).Scan(&lastSent)
	if !lastSent.IsZero() && time.Since(lastSent) < s.cfg.OTPResendCooldown {
		return nil, fmt.Errorf("%w: wait before requesting another code", ErrBadRequest)
	}
	otp, err := randomDigits(6)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expires := now.Add(s.cfg.OTPttl)
	_, err = s.pool.Exec(dbCtx, `
		INSERT INTO blink_otp_challenge (email, code_hash, expires_at, last_sent_at, attempts)
		VALUES ($1,$2,$3,$4,0)
		ON CONFLICT (email) DO UPDATE SET code_hash=EXCLUDED.code_hash, expires_at=EXCLUDED.expires_at,
			last_sent_at=EXCLUDED.last_sent_at, attempts=0
	`, email, hashHex(otp), expires, now)
	if err != nil {
		return nil, fmt.Errorf("%w: database busy — wait a moment and try again", ErrBadRequest)
	}

	mode := "smtp"
	if s.cfg.LocalMail() {
		mode = "local"
	} else if s.cfg.GmailConfigured() {
		mode = "gmail"
		if s.mail == nil {
			return nil, fmt.Errorf("%w: sign-in email is not configured", ErrBadRequest)
		}
		if err := s.mail.SendOTP(ctx, email, otp); err != nil {
			_, _ = s.pool.Exec(ctx, `DELETE FROM blink_otp_challenge WHERE email=$1`, email)
			log.Printf("OTP email failed for %s: %v", email, err)
			return nil, fmt.Errorf("%w: could not send sign-in email", ErrBadRequest)
		}
	} else if strings.TrimSpace(s.cfg.SMTPHost) == "" {
		return nil, fmt.Errorf("%w: sign-in email is not configured", ErrBadRequest)
	} else if s.mail != nil {
		if err := s.mail.SendOTP(ctx, email, otp); err != nil {
			_, _ = s.pool.Exec(ctx, `DELETE FROM blink_otp_challenge WHERE email=$1`, email)
			log.Printf("OTP email failed for %s: %v", email, err)
			return nil, fmt.Errorf("%w: could not send sign-in email", ErrBadRequest)
		}
	}

	resp := &OTPRequestResponse{
		Message:            "Check your email for a sign-in code.",
		DeliveryMode:       mode,
		ExpiresInSeconds:   int(s.cfg.OTPttl.Seconds()),
		ResendAfterSeconds: int(s.cfg.OTPResendCooldown.Seconds()),
	}
	if s.cfg.LocalMail() {
		resp.OTP = &otp
		resp.DeliveryMode = "local"
		resp.Message = "Demo code is shown below. Email OTP is off until SMTP is enabled."
	}
	return resp, nil
}

type OTPVerify struct {
	Email      string `json:"email"`
	OTP        string `json:"otp"`
	AccessCode string `json:"accessCode"`
}

type SessionResponse struct {
	Token     *string `json:"token"`
	Email     string  `json:"email"`
	ExpiresAt string  `json:"expiresAt"`
}

func (s *Service) VerifyOTP(ctx context.Context, req OTPVerify) (*SessionResponse, error) {
	email, err := s.normalizeAllowedEmail(req.Email)
	if err != nil {
		return nil, err
	}
	if err := s.checkGate(req.AccessCode); err != nil {
		return nil, err
	}
	otp := strings.TrimSpace(req.OTP)
	if len(otp) != 6 {
		return nil, fmt.Errorf("%w: invalid code", ErrBadRequest)
	}

	var codeHash string
	var expires time.Time
	var attempts int
	err = s.pool.QueryRow(ctx, `
		SELECT code_hash, expires_at, attempts FROM blink_otp_challenge WHERE email=$1
	`, email).Scan(&codeHash, &expires, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: request a code first", ErrBadRequest)
	}
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(expires) {
		return nil, fmt.Errorf("%w: code expired", ErrBadRequest)
	}
	if attempts >= s.cfg.OTPMaxAttempts {
		return nil, fmt.Errorf("%w: too many attempts", ErrBadRequest)
	}
	if !subtleHexEqual(codeHash, hashHex(otp)) {
		_, _ = s.pool.Exec(ctx, `UPDATE blink_otp_challenge SET attempts=attempts+1 WHERE email=$1`, email)
		return nil, fmt.Errorf("%w: invalid code", ErrBadRequest)
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM blink_otp_challenge WHERE email=$1`, email)

	raw, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	sessExp := time.Now().UTC().Add(s.cfg.SessionTTL)
	tokenHash := hashHex(raw)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_session (token_hash, email, expires_at) VALUES ($1,$2,$3)
	`, tokenHash, email, sessExp)
	if err != nil {
		return nil, err
	}
	s.rememberSession(tokenHash, Session{Email: email, ExpiresAt: sessExp})
	return &SessionResponse{Token: &raw, Email: email, ExpiresAt: sessExp.Format(time.RFC3339Nano)}, nil
}

func (s *Service) rememberSession(tokenHash string, sess Session) {
	// Keep auth off Neon for most of the session so polling + planner do not starve the pool.
	ttl := 45 * time.Minute
	if until := time.Until(sess.ExpiresAt); until > 0 && until < ttl {
		ttl = until
	}
	s.sessions.Store(tokenHash, cachedSession{sess: sess, until: time.Now().UTC().Add(ttl)})
}

func (s *Service) forgetSession(tokenHash string) {
	s.sessions.Delete(tokenHash)
}

func (s *Service) RequireSession(ctx context.Context, authorization string) (*Session, error) {
	token := bearer(authorization)
	if token == "" {
		return nil, ErrUnauthorized
	}
	tokenHash := hashHex(token)
	now := time.Now().UTC()
	if v, ok := s.sessions.Load(tokenHash); ok {
		cached := v.(cachedSession)
		if now.Before(cached.until) && now.Before(cached.sess.ExpiresAt) {
			out := cached.sess
			return &out, nil
		}
		s.sessions.Delete(tokenHash)
	}
	// Cold Neon wake can be slow; cache hits above avoid saturating the pool on every API call.
	dbCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var email string
	var expires time.Time
	err := s.pool.QueryRow(dbCtx, `
		SELECT email, expires_at FROM blink_session WHERE token_hash=$1
	`, tokenHash).Scan(&email, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded") {
			return nil, fmt.Errorf("%w: database busy — wait a moment and try again", ErrBadRequest)
		}
		return nil, err
	}
	if now.After(expires) {
		s.forgetSession(tokenHash)
		_, _ = s.pool.Exec(ctx, `DELETE FROM blink_session WHERE token_hash=$1`, tokenHash)
		return nil, ErrUnauthorized
	}
	sess := Session{Email: email, ExpiresAt: expires}
	s.rememberSession(tokenHash, sess)
	return &sess, nil
}

func (s *Service) Me(ctx context.Context, authorization string) (*SessionResponse, error) {
	sess, err := s.RequireSession(ctx, authorization)
	if err != nil {
		return nil, err
	}
	return &SessionResponse{Token: nil, Email: sess.Email, ExpiresAt: sess.ExpiresAt.Format(time.RFC3339Nano)}, nil
}

func (s *Service) Logout(ctx context.Context, authorization string) {
	token := bearer(authorization)
	if token == "" {
		return
	}
	tokenHash := hashHex(token)
	s.forgetSession(tokenHash)
	_, _ = s.pool.Exec(ctx, `DELETE FROM blink_session WHERE token_hash=$1`, tokenHash)
}

func (s *Service) checkGate(accessCode string) error {
	gate := strings.TrimSpace(s.cfg.LoginGate)
	if gate == "" {
		return nil
	}
	if strings.TrimSpace(accessCode) != gate {
		return fmt.Errorf("%w: invalid access code", ErrForbidden)
	}
	return nil
}

func (s *Service) normalizeAllowedEmail(raw string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: invalid email", ErrBadRequest)
	}
	email := strings.ToLower(addr.Address)
	at := strings.LastIndex(email, "@")
	if at <= 0 || at != strings.Index(email, "@") {
		return "", fmt.Errorf("%w: invalid email", ErrBadRequest)
	}
	domain := email[at+1:]
	if domain != strings.ToLower(s.cfg.LoginAllowedDomain) {
		return "", fmt.Errorf("%w: email domain not allowed", ErrForbidden)
	}
	return email, nil
}

func bearer(h string) string {
	h = strings.TrimSpace(h)
	if len(h) < 8 || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func hashHex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func subtleHexEqual(a, b string) bool {
	return strings.EqualFold(a, b)
}

func randomDigits(n int) (string, error) {
	var b strings.Builder
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		b.WriteByte(byte('0' + v.Int64()))
	}
	return b.String(), nil
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
