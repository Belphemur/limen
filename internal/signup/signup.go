// Package signup implements the public, tenant-agnostic SignupService
// Connect-RPC handler mounted at /api/limen.signup.v1.SignupService/*.
//
// Why this is a sibling of internal/admin instead of a skip-list on
// AdminService: there is no tenant on the URL to resolve, no portal
// cookie to verify, and no role to check. Encoding any of those as a
// "skip these methods" branch inside the AdminService interceptors
// forces every future reader to remember which RPCs are special. A
// separate service with no interceptors is the correct shape.
//
// The three RPCs implement an email-verification wizard:
//
//	StartSignup    — captcha + per-IP rate limit, mint+hash a
//	                 single-use verify token, INSERT pending_signups
//	                 row, send verification email. Returns an empty
//	                 envelope regardless of whether the email is new
//	                 or already used elsewhere (anti-enumeration).
//	VerifyEmail    — hash the supplied token, look up the row, set
//	                 EmailVerifiedAt, ROTATE the stored verify hash
//	                 to fresh random bytes (single-use), mint a
//	                 fresh completion_token, return its plaintext.
//	                 No cookie is set — the SPA carries the
//	                 completion_token forward, so signup works even
//	                 if the email link is clicked on a different
//	                 device than the one that started the wizard.
//	CompleteSignup — take the completion_token, require email
//	                 verified, run Zitadel org provisioning + Limen
//	                 tenant insert idempotently, mint a Zitadel
//	                 password-init code, return the password-init
//	                 URL the browser navigates to.
//
// Limen NEVER sees the user's plaintext password — Zitadel sets it on
// its hosted password-init UI. Zitadel is NOT touched in StartSignup
// or VerifyEmail; the org is only created when CompleteSignup runs.
package signup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/mailer"
	signupv1 "github.com/belphemur/limen/internal/signup/signupv1"
	"github.com/belphemur/limen/internal/signup/signupv1/signupv1connect"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/zitadel"
)

// outcomeTag is a closed enum used in the single structured log line
// per RPC. Keeping the value space closed makes alerting tractable.
type outcomeTag string

const (
	outcomeOK               outcomeTag = "ok"
	outcomeOKDuplicateEmail outcomeTag = "ok_duplicate_email"
	outcomeCaptchaFailed    outcomeTag = "captcha_failed"
	outcomeRateLimited      outcomeTag = "rate_limited"
	outcomeInvalidArgument  outcomeTag = "invalid_argument"
	outcomeTokenNotFound    outcomeTag = "token_not_found"
	outcomeTokenExpired     outcomeTag = "token_expired"
	outcomeAlreadyVerified  outcomeTag = "already_verified"
	outcomeEmailNotVerified outcomeTag = "email_not_verified"
	outcomeAlreadyCompleted outcomeTag = "already_completed"
	outcomeProvisionFailed  outcomeTag = "provision_failed"
	outcomeInternal         outcomeTag = "internal_error"
	outcomeFeatureDisabled  outcomeTag = "feature_disabled"
)

// passwordInitPath is appended to the configured Zitadel issuer to
// build the hosted password-init URL. Zitadel exposes this UI on a
// fixed path; callers do not need to override it.
const passwordInitPath = "/ui/login/password/init"

// Deps bundles every concrete dependency Service needs. Keeping it a
// struct of interfaces is overkill for a single implementation; tests
// substitute concrete values (real Postgres, captcha bypass verifier,
// nop mailer, fake Zitadel) instead.
type Deps struct {
	Store    *storage.Store
	Mailer   mailer.Mailer
	Template *mailer.SignupVerifyTemplate
	Zitadel  ZitadelClient
	Captcha  Verifier
	Limiter  Limiter
	Logger   *zap.Logger

	// Enabled gates the entire surface — false returns Unimplemented
	// to keep the SPA's /signup route disabled in production until
	// the deployment is ready.
	Enabled bool
	// BaseURL is the externally-reachable public origin (scheme +
	// host + optional port), used to build the verify link and the
	// password-init return URL.
	BaseURL string
	// ZitadelIssuer is the configured issuer URL used as the prefix
	// for the password-init link.
	ZitadelIssuer string
	// VerifyTokenTTL bounds VerifyEmail acceptance.
	VerifyTokenTTL time.Duration
	// TokenKey is the 32-byte HMAC key used to hash verify and
	// completion tokens before storage. Reuses
	// LIMEN_TOKEN_ENCRYPTION_KEY material via crypto.Key conversion
	// in the caller.
	TokenKey []byte
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// ZitadelClient is the small subset of *zitadel.Client the signup
// service uses. Phase 9h calls four methods; declaring an interface
// lets tests stub it without spinning up a real Zitadel.
type ZitadelClient interface {
	CreateOrganization(ctx context.Context, name string, seed *zitadel.SeedAdmin) (*zitadel.Organization, error)
	AddHumanUser(ctx context.Context, u zitadel.HumanUser) (string, error)
	AddUserGrant(ctx context.Context, orgID, userID string, roleKeys []string) (string, error)
	SetOrgMetadata(ctx context.Context, orgID, key string, value []byte) error
	PasswordReset(ctx context.Context, userID string) (string, error)
}

// Service is the SignupServiceHandler implementation.
type Service struct {
	deps Deps
}

// NewService builds the signup Connect-RPC service.
func NewService(deps Deps) *Service {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Captcha == nil {
		deps.Captcha = DevBypassVerifier{}
	}
	if deps.Limiter == nil {
		deps.Limiter = noopLimiter{}
	}
	if deps.VerifyTokenTTL == 0 {
		deps.VerifyTokenTTL = 24 * time.Hour
	}
	return &Service{deps: deps}
}

// Handler returns the URL-path-prefix + http.Handler pair. The
// handler ships with NO interceptors — these RPCs are public.
func (s *Service) Handler() (string, http.Handler) {
	return signupv1connect.NewSignupServiceHandler(s)
}

// StartSignup validates the form + captcha, rate-limits per-IP,
// records a pending signup, and emails a verification link.
func (s *Service) StartSignup(ctx context.Context, req *connect.Request[signupv1.StartSignupRequest]) (*connect.Response[signupv1.StartSignupResponse], error) {
	if !s.deps.Enabled {
		s.log(req, "", outcomeFeatureDisabled, nil)
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("signup is disabled"))
	}

	in := req.Msg
	tenantName := strings.TrimSpace(in.GetTenantName())
	givenName := strings.TrimSpace(in.GetOwnerGivenName())
	familyName := strings.TrimSpace(in.GetOwnerFamilyName())
	email := strings.TrimSpace(in.GetOwnerEmail())
	captchaToken := strings.TrimSpace(in.GetCaptchaToken())

	if tenantName == "" || givenName == "" || familyName == "" || email == "" {
		s.log(req, "", outcomeInvalidArgument, nil)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("tenant_name, owner_given_name, owner_family_name, and owner_email are required"))
	}
	if !looksLikeEmail(email) {
		s.log(req, "", outcomeInvalidArgument, nil)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("owner_email is not a valid address"))
	}

	ip := clientIP(req)
	if err := s.deps.Limiter.Allow(ctx, ip); err != nil {
		s.log(req, "", outcomeRateLimited, nil)
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many signups from this address; try again later"))
	}
	if err := s.deps.Captcha.Verify(ctx, captchaToken, ip); err != nil {
		s.log(req, "", outcomeCaptchaFailed, err)
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("captcha verification failed"))
	}

	emailLower := strings.ToLower(email)

	// Mint the verify token + its HMAC fingerprint. The plaintext
	// goes into the email; the fingerprint goes into the DB row.
	plain, hash, err := mintVerifyToken(s.deps.TokenKey)
	if err != nil {
		s.log(req, "", outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	row := &storage.PendingSignup{
		EmailLower:      emailLower,
		OwnerEmail:      email,
		OwnerGivenName:  givenName,
		OwnerFamilyName: familyName,
		TenantName:      tenantName,
		IP:              ip,
		VerifyTokenHash: hash,
	}

	db, commit, err := s.deps.Store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		s.log(req, "", outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if err := db.Create(row).Error; err != nil {
		_ = commit()
		s.log(req, "", outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if err := commit(); err != nil {
		s.log(req, row.PublicID, outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// Send the verification email. Failure must not leak — return the
	// empty success envelope unconditionally (anti-enumeration).
	verifyURL := s.buildVerifyURL(plain)
	htmlBody, textBody, err := s.deps.Template.Render(mailer.SignupVerifyData{
		TenantName: tenantName,
		OwnerName:  strings.TrimSpace(givenName + " " + familyName),
		VerifyURL:  verifyURL,
		ExpiresIn:  humanDuration(s.deps.VerifyTokenTTL),
	})
	if err != nil {
		s.log(req, row.PublicID, outcomeInternal, fmt.Errorf("render template: %w", err))
		return connect.NewResponse(&signupv1.StartSignupResponse{}), nil
	}
	if err := s.deps.Mailer.Send(ctx, email, "Verify your email to finish signing up", htmlBody, textBody); err != nil {
		s.log(req, row.PublicID, outcomeInternal, fmt.Errorf("send email: %w", err))
		return connect.NewResponse(&signupv1.StartSignupResponse{}), nil
	}

	s.log(req, row.PublicID, outcomeOK, nil)
	return connect.NewResponse(&signupv1.StartSignupResponse{}), nil
}

// VerifyEmail consumes the ?token= from the email link, marks the row
// verified, rotates the stored verify-token hash (single-use), and
// returns a fresh completion_token the SPA carries forward into
// CompleteSignup. No cookie is set — the wizard is cross-device safe.
func (s *Service) VerifyEmail(ctx context.Context, req *connect.Request[signupv1.VerifyEmailRequest]) (*connect.Response[signupv1.VerifyEmailResponse], error) {
	if !s.deps.Enabled {
		s.log(req, "", outcomeFeatureDisabled, nil)
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("signup is disabled"))
	}

	token := strings.TrimSpace(req.Msg.GetToken())
	if token == "" {
		s.log(req, "", outcomeInvalidArgument, nil)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("token is required"))
	}
	hash := hashVerifyToken(s.deps.TokenKey, token)

	db, commit, err := s.deps.Store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		s.log(req, "", outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	defer func() { _ = commit() }()

	var row storage.PendingSignup
	err = db.Where("verify_token_hash = ?", hash).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s.log(req, "", outcomeTokenNotFound, nil)
		return nil, connect.NewError(connect.CodeNotFound, errors.New("verification link is invalid or already used"))
	}
	if err != nil {
		s.log(req, "", outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	now := s.deps.Now()
	if now.Sub(row.CreatedAt) > s.deps.VerifyTokenTTL {
		s.log(req, row.PublicID, outcomeTokenExpired, nil)
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("verification link has expired"))
	}
	if row.CompletedAt != nil {
		s.log(req, row.PublicID, outcomeAlreadyCompleted, nil)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this signup has already been completed"))
	}

	// Rotate the verify token hash to fresh random bytes so the link
	// in the inbox becomes useless.
	_, rotated, err := mintVerifyToken(s.deps.TokenKey)
	if err != nil {
		s.log(req, row.PublicID, outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// Mint the completion token. Plaintext goes back to the SPA; the
	// hash is the only authority CompleteSignup will accept.
	completionToken, completionHash, err := mintVerifyToken(s.deps.TokenKey)
	if err != nil {
		s.log(req, row.PublicID, outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	row.VerifyTokenHash = rotated
	row.CompletionTokenHash = completionHash
	if row.EmailVerifiedAt == nil {
		row.EmailVerifiedAt = &now
	}
	if err := db.Save(&row).Error; err != nil {
		s.log(req, row.PublicID, outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	s.log(req, row.PublicID, outcomeOK, nil)
	return connect.NewResponse(&signupv1.VerifyEmailResponse{
		CompletionToken: completionToken,
	}), nil
}

// CompleteSignup provisions the Zitadel org + Limen tenant row,
// mints a password-init code, and returns the URL the browser
// navigates to. Idempotent on completed_at IS NOT NULL: a retry
// with the same completion_token replays cached results and mints
// a fresh password-init code.
func (s *Service) CompleteSignup(ctx context.Context, req *connect.Request[signupv1.CompleteSignupRequest]) (*connect.Response[signupv1.CompleteSignupResponse], error) {
	if !s.deps.Enabled {
		s.log(req, "", outcomeFeatureDisabled, nil)
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("signup is disabled"))
	}

	token := strings.TrimSpace(req.Msg.GetCompletionToken())
	if token == "" {
		s.log(req, "", outcomeInvalidArgument, nil)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("completion_token is required"))
	}
	hash := hashVerifyToken(s.deps.TokenKey, token)

	db, commit, err := s.deps.Store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		s.log(req, "", outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	defer func() { _ = commit() }()

	var row storage.PendingSignup
	err = db.Where("completion_token_hash = ?", hash).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s.log(req, "", outcomeTokenNotFound, nil)
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired completion token"))
	}
	if err != nil {
		s.log(req, "", outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	publicID := row.PublicID
	if row.EmailVerifiedAt == nil {
		s.log(req, publicID, outcomeEmailNotVerified, nil)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("email not verified"))
	}

	// Idempotent replay path: if CompleteSignup has already provisioned
	// the tenant, return the same tenant public id and mint a fresh
	// password-init code (the previous one may have been used or
	// expired by Zitadel).
	if row.CompletedAt != nil && row.TenantID != nil {
		return s.replayCompletion(ctx, db, &row, req)
	}

	// First-time provisioning path.
	org, err := s.deps.Zitadel.CreateOrganization(ctx, row.TenantName, nil)
	if err != nil {
		s.log(req, publicID, outcomeProvisionFailed, fmt.Errorf("create org: %w", err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("provisioning failed"))
	}

	userID, err := s.deps.Zitadel.AddHumanUser(ctx, zitadel.HumanUser{
		Email:         row.OwnerEmail,
		GivenName:     row.OwnerGivenName,
		FamilyName:    row.OwnerFamilyName,
		OrgID:         org.ID,
		EmailVerified: true,
	})
	if err != nil {
		s.log(req, publicID, outcomeProvisionFailed, fmt.Errorf("add human user: %w", err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("provisioning failed"))
	}
	if _, err := s.deps.Zitadel.AddUserGrant(ctx, org.ID, userID, []string{"owner"}); err != nil {
		// Treat ALREADY_EXISTS as success (Zitadel grant is idempotent).
		if !strings.Contains(err.Error(), "ALREADY_EXISTS") && !strings.Contains(err.Error(), "AlreadyExists") {
			s.log(req, publicID, outcomeProvisionFailed, fmt.Errorf("add user grant: %w", err))
			return nil, connect.NewError(connect.CodeInternal, errors.New("provisioning failed"))
		}
	}

	tenant := &storage.Tenant{
		Name:         row.TenantName,
		ZitadelOrgID: org.ID,
	}
	user := &storage.User{
		Email:          row.OwnerEmail,
		Name:           strings.TrimSpace(row.OwnerGivenName + " " + row.OwnerFamilyName),
		ZitadelSubject: userID,
	}
	if err := persistTenantAndOwner(db, tenant, user); err != nil {
		s.log(req, publicID, outcomeProvisionFailed, fmt.Errorf("persist tenant: %w", err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("provisioning failed"))
	}

	// Mirror the tenant public id into Zitadel org metadata. Failure is
	// non-fatal — the local row is already authoritative.
	if err := s.deps.Zitadel.SetOrgMetadata(ctx, org.ID, "limen_tenant_id", []byte(tenant.PublicID)); err != nil {
		s.deps.Logger.Warn("signup: failed to mirror tenant public id to Zitadel org metadata",
			zap.String("signup.id", publicID),
			zap.String("zitadel.org_id", org.ID),
			zap.Error(err))
	}

	code, err := s.deps.Zitadel.PasswordReset(ctx, userID)
	if err != nil {
		s.log(req, publicID, outcomeProvisionFailed, fmt.Errorf("password reset: %w", err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("provisioning failed"))
	}

	// Flip the row to completed.
	now := s.deps.Now()
	row.CompletedAt = &now
	row.ZitadelOrgID = org.ID
	row.ZitadelUserID = userID
	row.TenantID = &tenant.ID
	if err := db.Save(&row).Error; err != nil {
		// Tenant is already provisioned in Zitadel + Limen; surface
		// the error but don't trigger a rollback of provisioning.
		s.log(req, publicID, outcomeInternal, fmt.Errorf("mark complete: %w", err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	resp := connect.NewResponse(&signupv1.CompleteSignupResponse{
		TenantPublicId:  tenant.PublicID,
		PasswordInitUrl: s.buildPasswordInitURL(userID, code, tenant.PublicID),
	})
	s.log(req, publicID, outcomeOK, nil)
	return resp, nil
}

// replayCompletion serves a CompleteSignup retry against an
// already-completed row: it looks up the tenant public id and mints
// a fresh password-init code so the user can finish setting their
// password even if the original code expired.
func (s *Service) replayCompletion(ctx context.Context, db *gorm.DB, row *storage.PendingSignup, req *connect.Request[signupv1.CompleteSignupRequest]) (*connect.Response[signupv1.CompleteSignupResponse], error) {
	var t storage.Tenant
	if err := db.Where("id = ?", *row.TenantID).First(&t).Error; err != nil {
		s.log(req, row.PublicID, outcomeInternal, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	code, err := s.deps.Zitadel.PasswordReset(ctx, row.ZitadelUserID)
	if err != nil {
		s.log(req, row.PublicID, outcomeProvisionFailed, fmt.Errorf("password reset (replay): %w", err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("provisioning failed"))
	}
	resp := connect.NewResponse(&signupv1.CompleteSignupResponse{
		TenantPublicId:  t.PublicID,
		PasswordInitUrl: s.buildPasswordInitURL(row.ZitadelUserID, code, t.PublicID),
	})
	s.log(req, row.PublicID, outcomeAlreadyCompleted, nil)
	return resp, nil
}

// buildVerifyURL composes the absolute URL the user clicks in the
// verification email.
func (s *Service) buildVerifyURL(plainToken string) string {
	base := strings.TrimRight(s.deps.BaseURL, "/")
	return base + "/signup/verify?token=" + url.QueryEscape(plainToken)
}

// buildPasswordInitURL composes the Zitadel-hosted password-init URL
// the browser navigates to after CompleteSignup succeeds. The
// returnURL drops the user at /auth/login?tenant=<pid>&return_to=...
// so the OIDC RP completes the sign-in flow against the new org.
func (s *Service) buildPasswordInitURL(userID, code, tenantPublicID string) string {
	base := strings.TrimRight(s.deps.BaseURL, "/")
	returnURL := base + "/auth/login?tenant=" + url.QueryEscape(tenantPublicID) +
		"&return_to=" + url.QueryEscape("/t/"+tenantPublicID+"/admin/")
	issuer := strings.TrimRight(s.deps.ZitadelIssuer, "/")
	q := url.Values{}
	q.Set("userID", userID)
	q.Set("code", code)
	q.Set("returnURL", returnURL)
	return issuer + passwordInitPath + "?" + q.Encode()
}

// log emits a single structured line per RPC with a closed outcome
// enum. err is logged at Warn for non-OK outcomes that carry an
// underlying error.
func (s *Service) log(req connect.AnyRequest, signupID string, outcome outcomeTag, err error) {
	fields := []zap.Field{
		zap.String("signup.outcome", string(outcome)),
		zap.String("signup.ip", clientIP(req)),
	}
	if signupID != "" {
		fields = append(fields, zap.String("signup.id", signupID))
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
		s.deps.Logger.Warn("signup rpc", fields...)
		return
	}
	s.deps.Logger.Info("signup rpc", fields...)
}

// persistTenantAndOwner is the in-transaction equivalent of the
// CLI's persistTenantAndOwner. It is kept local to the signup
// package because the call shape here is "we just provisioned the
// org" rather than "we are CLI-binding to a pre-existing org".
func persistTenantAndOwner(tx *gorm.DB, tenant *storage.Tenant, user *storage.User) error {
	return tx.Transaction(func(t *gorm.DB) error {
		var existing storage.Tenant
		switch err := t.Where("zitadel_org_id = ?", tenant.ZitadelOrgID).First(&existing).Error; {
		case err == nil:
			*tenant = existing
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := t.Create(tenant).Error; err != nil {
				return fmt.Errorf("insert tenant: %w", err)
			}
		default:
			return fmt.Errorf("lookup tenant: %w", err)
		}
		user.TenantID = tenant.ID
		var existingUser storage.User
		switch err := t.Where("tenant_id = ? AND zitadel_subject = ?", tenant.ID, user.ZitadelSubject).First(&existingUser).Error; {
		case err == nil:
			*user = existingUser
			return nil
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := t.Create(user).Error; err != nil {
				return fmt.Errorf("insert owner user: %w", err)
			}
			return nil
		default:
			return fmt.Errorf("lookup owner user: %w", err)
		}
	})
}

// clientIP best-effort extracts the request IP. Connect-RPC carries
// X-Forwarded-For when fronted by a reverse proxy; falls back to the
// X-Real-Ip header. Returns "" when neither is present (server-to-
// server invocation in tests).
func clientIP(req connect.AnyRequest) string {
	if v := req.Header().Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i >= 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := req.Header().Get("X-Real-Ip"); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// looksLikeEmail is a deliberately loose check — strict RFC 5322
// validation is the job of the SMTP relay, not the signup form.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return strings.ContainsRune(s[at+1:], '.')
}

// humanDuration renders a Duration like "24h" → "24 hours" for the
// email body. Falls back to Duration.String() for anything other
// than a whole hour or minute.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return d.String()
	}
	if d%time.Hour == 0 {
		h := int(d / time.Hour)
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
	if d%time.Minute == 0 {
		m := int(d / time.Minute)
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	}
	return d.String()
}
