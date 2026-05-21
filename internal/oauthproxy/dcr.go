package oauthproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"
)

// appManager is the consumer-side ISP slice of *zitadel.Client that the DCR
// proxy needs. Keeps oauthproxy decoupled from the SDK surface and trivial
// to fake in tests.
type appManager interface {
	AddOIDCApp(ctx context.Context, in zitadel.AddOIDCAppInput) (*zitadel.OIDCApp, error)
	UpdateOIDCApp(ctx context.Context, in zitadel.UpdateOIDCAppInput) error
	DeleteOIDCApp(ctx context.Context, orgID, projectID, appID string) error
	GetOIDCApp(ctx context.Context, orgID, projectID, appID string) (*zitadel.OIDCApp, error)
	// EnsureProject is the JIT find-or-create used by DCR to give each
	// MCP client a dedicated Zitadel project under the tenant org. See
	// docs/phases/phase-07b-dcr-per-client-project.md.
	EnsureProject(ctx context.Context, orgID, name string) (string, error)
}

// DCRConfig configures the DCR proxy handler.
type DCRConfig struct {
	// DCREnabled is the global kill-switch (config-level). Per-tenant
	// gating still happens via Tenant.DCREnabled.
	DCREnabled bool
	// InitialAccessToken, when non-empty, is required on POST /register
	// (RFC 7591 §3).
	InitialAccessToken string
	// BaseURL is the public origin of the Limen deployment, used to build
	// `registration_client_uri` (e.g. https://limen.example.com).
	BaseURL string
}

// DCRHandler implements the OAuth 2.0 Dynamic Client Registration proxy
// (POST /register) and the RFC 7592 management endpoints
// (GET/PUT/DELETE /register/{client_id}). The handler must run behind
// tenancy.RequireTenant.
type DCRHandler struct {
	cfg     DCRConfig
	store   *storage.Store
	apps    appManager
	logger  *zap.Logger
	baseURL string
}

// NewDCRHandler validates the config and returns a ready-to-mount handler.
func NewDCRHandler(cfg DCRConfig, store *storage.Store, apps appManager, logger *zap.Logger) (*DCRHandler, error) {
	if store == nil {
		return nil, errors.New("oauthproxy: store is required")
	}
	if apps == nil {
		return nil, errors.New("oauthproxy: appManager is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("oauthproxy: BaseURL is required")
	}
	return &DCRHandler{
		cfg:     cfg,
		store:   store,
		apps:    apps,
		logger:  logger,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
	}, nil
}

// dcrRequest is the subset of OAuth 2.0 DCR (RFC 7591) metadata Limen
// accepts. RFC 7591 §2 requires authorization servers to ignore unknown
// client metadata, so the decoder is tolerant — fields not listed here
// are silently dropped after decode rather than rejected.
type dcrRequest struct {
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	ApplicationType         string   `json:"application_type,omitempty"`
	SoftwareID              string   `json:"software_id,omitempty"`
	SoftwareVersion         string   `json:"software_version,omitempty"`
	PostLogoutRedirectURIs  []string `json:"post_logout_redirect_uris,omitempty"`

	// Accepted-and-passed-through descriptive metadata. Not validated and
	// not currently surfaced to Zitadel, but parsed so spec-compliant
	// clients (Cursor, Claude Desktop, MCP Inspector) don't fail on
	// otherwise standard fields.
	ClientURI string   `json:"client_uri,omitempty"`
	LogoURI   string   `json:"logo_uri,omitempty"`
	TOSURI    string   `json:"tos_uri,omitempty"`
	PolicyURI string   `json:"policy_uri,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	Contacts  []string `json:"contacts,omitempty"`
	JwksURI   string   `json:"jwks_uri,omitempty"`
	Jwks      any      `json:"jwks,omitempty"`
}

// dcrResponse is the RFC 7591 §3.2.1 success body Limen returns on POST
// /register and GET /register/{client_id}. RegistrationAccessToken is
// populated only on POST.
type dcrResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ApplicationType         string   `json:"application_type"`
	SoftwareID              string   `json:"software_id,omitempty"`
	SoftwareVersion         string   `json:"software_version,omitempty"`
	PostLogoutRedirectURIs  []string `json:"post_logout_redirect_uris,omitempty"`
	RegistrationAccessToken string   `json:"registration_access_token,omitempty"`
	RegistrationClientURI   string   `json:"registration_client_uri,omitempty"`
}

// Register handles POST /t/{tenant}/oauth/register.
func (h *DCRHandler) Register(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenancy.TenantFromContext(r.Context())
	if !ok {
		http.Error(w, "tenant not resolved", http.StatusInternalServerError)
		return
	}
	if !h.cfg.DCREnabled {
		h.dcrFail(w, r, "register", http.StatusForbidden, "invalid_client_metadata", "dynamic client registration is disabled")
		return
	}
	if !tenant.DCREnabled {
		h.dcrFail(w, r, "register", http.StatusForbidden, "invalid_client_metadata", "dynamic client registration is disabled for this tenant")
		return
	}
	if h.cfg.InitialAccessToken != "" {
		got := extractBearer(r)
		if subtle.ConstantTimeCompare([]byte(got), []byte(h.cfg.InitialAccessToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dcr"`)
			h.dcrFail(w, r, "register", http.StatusUnauthorized, "invalid_token", "initial access token required")
			return
		}
	}

	req, err := decodeDCRRequest(r)
	if err != nil {
		h.dcrFail(w, r, "register", http.StatusBadRequest, "invalid_client_metadata", err.Error(), zap.String("stage", "decode"))
		return
	}
	normalized, zitadelInput, err := h.normalize(tenant, req)
	if err != nil {
		h.dcrFail(w, r, "register", http.StatusBadRequest, "invalid_client_metadata", err.Error(), zap.String("stage", "normalize"))
		return
	}

	projectID, err := h.apps.EnsureProject(r.Context(), tenant.ZitadelOrgID, normalized.ClientName)
	if err != nil {
		h.logger.Error("zitadel project ensure failed",
			zap.String("tenant", tenant.PublicID),
			zap.String("client_name", normalized.ClientName),
			zap.Error(err))
		dcrError(w, http.StatusBadGateway, "server_error", "upstream identity provider rejected the registration")
		return
	}
	zitadelInput.ProjectID = projectID

	app, err := h.apps.AddOIDCApp(r.Context(), zitadelInput)
	if err != nil {
		h.logger.Error("zitadel app create failed", zap.String("tenant", tenant.PublicID), zap.Error(err))
		dcrError(w, http.StatusBadGateway, "server_error", "upstream identity provider rejected the registration")
		return
	}

	rawToken, tokenHash, err := newRegistrationAccessToken()
	if err != nil {
		h.logger.Error("registration access token mint failed", zap.Error(err))
		_ = h.apps.DeleteOIDCApp(r.Context(), tenant.ZitadelOrgID, projectID, app.AppID)
		dcrError(w, http.StatusInternalServerError, "server_error", "failed to mint registration access token")
		return
	}

	row := storage.ZitadelApp{
		TenantID:                    tenant.ID,
		ZitadelAppID:                app.AppID,
		ZitadelProjectID:            projectID,
		ClientID:                    app.ClientID,
		Name:                        normalized.ClientName,
		RedirectURIs:                strings.Join(normalized.RedirectURIs, "\n"),
		SoftwareID:                  normalized.SoftwareID,
		SoftwareVersion:             normalized.SoftwareVersion,
		RegistrationAccessTokenHash: tokenHash,
	}
	if app.ClientSecret != "" {
		row.ClientSecret = crypto.NewSecret([]byte(app.ClientSecret))
	}
	if err := h.persist(r.Context(), &row); err != nil {
		h.logger.Error("mirror persist failed; rolling back zitadel app",
			zap.String("tenant", tenant.PublicID), zap.String("app_id", app.AppID), zap.Error(err))
		_ = h.apps.DeleteOIDCApp(r.Context(), tenant.ZitadelOrgID, projectID, app.AppID)
		dcrError(w, http.StatusInternalServerError, "server_error", "failed to persist client registration")
		return
	}

	resp := h.buildResponse(tenant.PublicID, app, normalized, rawToken)
	writeJSON(w, http.StatusCreated, resp)
}

// Get handles GET /t/{tenant}/oauth/register/{client_id}.
func (h *DCRHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenant, row, authedOK := h.authManagement(w, r)
	if !authedOK {
		return
	}
	app, err := h.apps.GetOIDCApp(r.Context(), tenant.ZitadelOrgID, row.ZitadelProjectID, row.ZitadelAppID)
	if err != nil {
		h.logger.Error("zitadel app get failed", zap.String("tenant", tenant.PublicID), zap.Error(err))
		dcrError(w, http.StatusBadGateway, "server_error", "upstream identity provider unavailable")
		return
	}
	normalized := dcrRequest{
		ClientName:              row.Name,
		RedirectURIs:            app.RedirectURIs,
		PostLogoutRedirectURIs:  app.PostLogoutRedirectURIs,
		ApplicationType:         string(app.AppType),
		TokenEndpointAuthMethod: string(app.AuthMethod),
		SoftwareID:              row.SoftwareID,
		SoftwareVersion:         row.SoftwareVersion,
	}
	resp := h.buildResponse(tenant.PublicID, app, normalized, "")
	resp.ClientIDIssuedAt = row.CreatedAt.Unix()
	writeJSON(w, http.StatusOK, resp)
}

// Put handles PUT /t/{tenant}/oauth/register/{client_id} (RFC 7592 full
// replace). Limen pins grant_types and response_types so those keys must
// match the canonical pair if present in the request.
func (h *DCRHandler) Put(w http.ResponseWriter, r *http.Request) {
	tenant, row, authedOK := h.authManagement(w, r)
	if !authedOK {
		return
	}
	req, err := decodeDCRRequest(r)
	if err != nil {
		h.dcrFail(w, r, "put", http.StatusBadRequest, "invalid_client_metadata", err.Error(), zap.String("stage", "decode"), zap.String("client_id", row.ClientID))
		return
	}
	normalized, _, err := h.normalize(tenant, req)
	if err != nil {
		h.dcrFail(w, r, "put", http.StatusBadRequest, "invalid_client_metadata", err.Error(), zap.String("stage", "normalize"), zap.String("client_id", row.ClientID))
		return
	}
	if err := h.apps.UpdateOIDCApp(r.Context(), zitadel.UpdateOIDCAppInput{
		OrgID:                  tenant.ZitadelOrgID,
		ProjectID:              row.ZitadelProjectID,
		AppID:                  row.ZitadelAppID,
		RedirectURIs:           normalized.RedirectURIs,
		PostLogoutRedirectURIs: normalized.PostLogoutRedirectURIs,
		AppType:                zitadel.OIDCAppType(normalized.ApplicationType),
		AuthMethod:             zitadel.OIDCAuthMethod(normalized.TokenEndpointAuthMethod),
	}); err != nil {
		h.logger.Error("zitadel app update failed", zap.String("tenant", tenant.PublicID), zap.Error(err))
		dcrError(w, http.StatusBadGateway, "server_error", "upstream identity provider rejected the update")
		return
	}
	row.Name = normalized.ClientName
	row.RedirectURIs = strings.Join(normalized.RedirectURIs, "\n")
	row.SoftwareID = normalized.SoftwareID
	row.SoftwareVersion = normalized.SoftwareVersion
	if err := h.update(r.Context(), row); err != nil {
		h.logger.Error("mirror update failed", zap.String("tenant", tenant.PublicID), zap.Error(err))
		dcrError(w, http.StatusInternalServerError, "server_error", "failed to persist client update")
		return
	}
	app, err := h.apps.GetOIDCApp(r.Context(), tenant.ZitadelOrgID, row.ZitadelProjectID, row.ZitadelAppID)
	if err != nil {
		h.logger.Error("zitadel app reread failed", zap.String("tenant", tenant.PublicID), zap.Error(err))
		dcrError(w, http.StatusBadGateway, "server_error", "upstream identity provider unavailable")
		return
	}
	resp := h.buildResponse(tenant.PublicID, app, normalized, "")
	resp.ClientIDIssuedAt = row.CreatedAt.Unix()
	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE /t/{tenant}/oauth/register/{client_id}.
func (h *DCRHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenant, row, authedOK := h.authManagement(w, r)
	if !authedOK {
		return
	}
	if err := h.apps.DeleteOIDCApp(r.Context(), tenant.ZitadelOrgID, row.ZitadelProjectID, row.ZitadelAppID); err != nil {
		h.logger.Error("zitadel app delete failed", zap.String("tenant", tenant.PublicID), zap.Error(err))
		dcrError(w, http.StatusBadGateway, "server_error", "upstream identity provider rejected the delete")
		return
	}
	if err := h.softDelete(r.Context(), row); err != nil {
		h.logger.Error("mirror delete failed", zap.String("tenant", tenant.PublicID), zap.Error(err))
		dcrError(w, http.StatusInternalServerError, "server_error", "failed to record client deletion")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authManagement is the shared preamble for the three RFC 7592 endpoints:
// it loads the ZitadelApp row by {client_id} for the request's tenant and
// constant-time-compares the bearer token against the persisted hash.
//
// Returns (tenant, row, true) on success; on failure it writes the response
// and returns (_, _, false). 404 is reused for both "no such client" and
// "bad token" so management endpoints don't leak client_id existence.
func (h *DCRHandler) authManagement(w http.ResponseWriter, r *http.Request) (*storage.Tenant, *storage.ZitadelApp, bool) {
	tenant, ok := tenancy.TenantFromContext(r.Context())
	if !ok {
		http.Error(w, "tenant not resolved", http.StatusInternalServerError)
		return nil, nil, false
	}
	clientID := chi.URLParam(r, "client_id")
	if clientID == "" {
		http.NotFound(w, r)
		return nil, nil, false
	}
	bearer := extractBearer(r)
	if bearer == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dcr"`)
		h.dcrFail(w, r, "auth", http.StatusUnauthorized, "invalid_token", "registration access token required", zap.String("client_id", clientID))
		return nil, nil, false
	}
	row, err := h.loadByClientID(r.Context(), tenant.ID, clientID)
	if err != nil {
		h.logger.Warn("dcr management: client_id not found",
			zap.String("tenant", tenant.PublicID), zap.String("client_id", clientID), zap.Error(err))
		http.NotFound(w, r)
		return nil, nil, false
	}
	got := sha256.Sum256([]byte(bearer))
	if subtle.ConstantTimeCompare(got[:], row.RegistrationAccessTokenHash) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dcr"`)
		h.dcrFail(w, r, "auth", http.StatusUnauthorized, "invalid_token", "registration access token mismatch", zap.String("client_id", clientID))
		return nil, nil, false
	}
	return tenant, row, true
}

// normalize validates an incoming dcrRequest against Limen's policy floor +
// the tenant's redirect-URI allowlist, applies defaults, and returns both
// the normalized DCR form (for response + persistence) and the
// Zitadel-shaped input for the create / update call.
func (h *DCRHandler) normalize(tenant *storage.Tenant, req dcrRequest) (dcrRequest, zitadel.AddOIDCAppInput, error) {
	if len(req.RedirectURIs) == 0 {
		return dcrRequest{}, zitadel.AddOIDCAppInput{}, errors.New("redirect_uris is required")
	}

	allow, err := CompilePatternSet(tenant.DCRRedirectURIAllowlist)
	if err != nil {
		return dcrRequest{}, zitadel.AddOIDCAppInput{}, fmt.Errorf("tenant allowlist invalid: %w", err)
	}
	for _, u := range req.RedirectURIs {
		if err := allow.CheckRedirectURI(u); err != nil {
			h.logger.Warn("DCR redirect_uri rejected",
				zap.String("tenant_id", tenant.PublicID),
				zap.String("redirect_uri", u),
				zap.Strings("patterns", tenant.DCRRedirectURIAllowlist),
				zap.Error(err),
			)
			return dcrRequest{}, zitadel.AddOIDCAppInput{}, fmt.Errorf("redirect_uri %q: %w", u, err)
		}
	}

	// grant_types ⊆ {authorization_code, refresh_token}; default = both.
	if len(req.GrantTypes) == 0 {
		req.GrantTypes = []string{"authorization_code", "refresh_token"}
	} else {
		for _, g := range req.GrantTypes {
			if g != "authorization_code" && g != "refresh_token" {
				return dcrRequest{}, zitadel.AddOIDCAppInput{}, fmt.Errorf("grant_type %q not supported", g)
			}
		}
	}

	// response_types = {code}; default = {code}.
	if len(req.ResponseTypes) == 0 {
		req.ResponseTypes = []string{"code"}
	} else if len(req.ResponseTypes) != 1 || req.ResponseTypes[0] != "code" {
		return dcrRequest{}, zitadel.AddOIDCAppInput{}, errors.New("response_types must be [\"code\"]")
	}

	// token_endpoint_auth_method ∈ {none, client_secret_basic}; default = none.
	switch req.TokenEndpointAuthMethod {
	case "":
		req.TokenEndpointAuthMethod = "none"
	case "none", "client_secret_basic":
	default:
		return dcrRequest{}, zitadel.AddOIDCAppInput{}, fmt.Errorf("token_endpoint_auth_method %q not supported", req.TokenEndpointAuthMethod)
	}

	// application_type defaults to "native".
	switch req.ApplicationType {
	case "":
		req.ApplicationType = "native"
	case "native", "web":
	default:
		return dcrRequest{}, zitadel.AddOIDCAppInput{}, fmt.Errorf("application_type %q not supported", req.ApplicationType)
	}

	if strings.TrimSpace(req.ClientName) == "" {
		req.ClientName = "Unnamed MCP client"
	}

	zin := zitadel.AddOIDCAppInput{
		OrgID: tenant.ZitadelOrgID,
		// Zitadel enforces unique application names within a project.
		// DCR has no such uniqueness requirement (RFC 7591 lets many
		// clients register with the same client_name), so we suffix the
		// upstream name with random bytes and keep the original
		// client_name in the DCR response + our mirror row.
		Name:                   uniqueZitadelAppName(req.ClientName),
		RedirectURIs:           req.RedirectURIs,
		PostLogoutRedirectURIs: req.PostLogoutRedirectURIs,
		AppType:                zitadel.OIDCAppType(req.ApplicationType),
		AuthMethod:             zitadel.OIDCAuthMethod(req.TokenEndpointAuthMethod),
	}
	return req, zin, nil
}

// uniqueZitadelAppName appends a ULID suffix to the requested client_name
// to dodge Zitadel's per-project app-name uniqueness check.
func uniqueZitadelAppName(base string) string {
	return fmt.Sprintf("%s [%s]", base, ulid.Make().String())
}

func (h *DCRHandler) buildResponse(tenantPublicID string, app *zitadel.OIDCApp, normalized dcrRequest, registrationToken string) dcrResponse {
	return dcrResponse{
		ClientID:                app.ClientID,
		ClientSecret:            app.ClientSecret,
		ClientIDIssuedAt:        time.Now().UTC().Unix(),
		ClientSecretExpiresAt:   0,
		ClientName:              normalized.ClientName,
		RedirectURIs:            app.RedirectURIs,
		PostLogoutRedirectURIs:  app.PostLogoutRedirectURIs,
		GrantTypes:              normalized.GrantTypes,
		ResponseTypes:           normalized.ResponseTypes,
		TokenEndpointAuthMethod: normalized.TokenEndpointAuthMethod,
		ApplicationType:         normalized.ApplicationType,
		SoftwareID:              normalized.SoftwareID,
		SoftwareVersion:         normalized.SoftwareVersion,
		RegistrationAccessToken: registrationToken,
		RegistrationClientURI:   h.baseURL + "/t/" + tenantPublicID + "/oauth/register/" + app.ClientID,
	}
}

// persist / update / softDelete / loadByClientID isolate the GORM access so
// the public methods stay readable. All run on the tenant-scoped session.

func (h *DCRHandler) persist(ctx context.Context, row *storage.ZitadelApp) error {
	tx, commit, err := h.store.Session(ctx)
	if err != nil {
		return err
	}
	if err := tx.Create(row).Error; err != nil {
		_ = commit() // rolls back since tx.Error is set
		return err
	}
	return commit()
}

func (h *DCRHandler) update(ctx context.Context, row *storage.ZitadelApp) error {
	tx, commit, err := h.store.Session(ctx)
	if err != nil {
		return err
	}
	if err := tx.Save(row).Error; err != nil {
		_ = commit()
		return err
	}
	return commit()
}

func (h *DCRHandler) softDelete(ctx context.Context, row *storage.ZitadelApp) error {
	tx, commit, err := h.store.Session(ctx)
	if err != nil {
		return err
	}
	if err := tx.Delete(row).Error; err != nil {
		_ = commit()
		return err
	}
	return commit()
}

func (h *DCRHandler) loadByClientID(ctx context.Context, tenantID int64, clientID string) (*storage.ZitadelApp, error) {
	_ = tenantID // tenant pinned on ctx by tenancy middleware; RLS scopes the SELECT
	tx, commit, err := h.store.Session(ctx)
	if err != nil {
		return nil, err
	}
	var row storage.ZitadelApp
	err = tx.Where("client_id = ?", clientID).First(&row).Error
	commitErr := commit()
	if err != nil {
		return nil, err
	}
	if commitErr != nil {
		return nil, commitErr
	}
	return &row, nil
}

// decodeDCRRequest reads the JSON body. RFC 7591 §2 requires the
// authorization server to ignore unknown client metadata, so the
// decoder is tolerant — extra fields are dropped on the floor.
func decodeDCRRequest(r *http.Request) (dcrRequest, error) {
	if !isJSONContentType(r) {
		return dcrRequest{}, errors.New("Content-Type must be application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16))
	var req dcrRequest
	if err := dec.Decode(&req); err != nil {
		return dcrRequest{}, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return dcrRequest{}, errors.New("unexpected trailing JSON")
	}
	return req, nil
}

func isJSONContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "application/json")
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func newRegistrationAccessToken() (string, []byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func dcrError(w http.ResponseWriter, status int, code, description string) {
	body := map[string]string{"error": code, "error_description": description}
	writeJSON(w, status, body)
}

// dcrFail writes an RFC 7591 error response and emits a structured log
// line so 4xx outcomes are visible in operator logs. The caller passes
// the operation name ("register", "get", "put", "delete") plus any
// extra zap fields worth surfacing.
func (h *DCRHandler) dcrFail(w http.ResponseWriter, r *http.Request, op string, status int, code, description string, extra ...zap.Field) {
	fields := make([]zap.Field, 0, 5+len(extra))
	fields = append(fields,
		zap.String("op", op),
		zap.Int("status", status),
		zap.String("error", code),
		zap.String("error_description", description),
	)
	if tenant, ok := tenancy.TenantFromContext(r.Context()); ok {
		fields = append(fields, zap.String("tenant", tenant.PublicID))
	}
	fields = append(fields, extra...)
	h.logger.Warn("dcr request rejected", fields...)
	dcrError(w, status, code, description)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
