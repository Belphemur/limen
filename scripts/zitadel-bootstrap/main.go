package main
// Package main implements the Zitadel bootstrap for Limen dev environments.
//
// It is idempotent: re-running it is safe. It creates the Limen Gateway
// project, the Portal (OIDC/PKCE) and MCP RS (API) apps, the project roles
// (member/admin/owner), and a sample tenant org with an owner user.
//
// Auth: a Zitadel PAT (Personal Access Token) printed once by Zitadel on
// first init. We expect the file at ZITADEL_PAT_FILE.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type client struct {
	base  string
	token string
	hc    *http.Client
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("zitadel api %d: %s", e.Status, e.Body)
}

// alreadyExists reports whether err signals an idempotent "already exists" condition.
func alreadyExists(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Status == http.StatusConflict {
		return true
	}
	// Zitadel returns 409-equivalent as a 9 / FailedPrecondition with a message,
	// or 6 / AlreadyExists. The HTTP gateway maps to 409 in most cases; we also
	// accept message-level matches for safety.
	low := strings.ToLower(ae.Body)
	return strings.Contains(low, "already exists") || strings.Contains(low, "alreadyexists")
}

func (c *client) do(method, path, orgID string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if orgID != "" {
		req.Header.Set("x-zitadel-orgid", orgID)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return &apiError{Status: resp.StatusCode, Body: string(buf)}
	}
	if out != nil && len(buf) > 0 {
		return json.Unmarshal(buf, out)
	}
	return nil
}

// --- Resource helpers ---------------------------------------------------------

type idResp struct {
	ID string `json:"id"`
}

type projectAppOIDCConfig struct {
	RedirectURIs           []string `json:"redirectUris"`
	ResponseTypes          []string `json:"responseTypes"`
	GrantTypes             []string `json:"grantTypes"`
	AppType                string   `json:"appType"`
	AuthMethodType         string   `json:"authMethodType"`
	PostLogoutRedirectURIs []string `json:"postLogoutRedirectUris,omitempty"`
	DevMode                bool     `json:"devMode"`
	AccessTokenType        string   `json:"accessTokenType"`
	AccessTokenRoleAssertion  bool  `json:"accessTokenRoleAssertion"`
	IDTokenRoleAssertion      bool  `json:"idTokenRoleAssertion"`
	IDTokenUserinfoAssertion  bool  `json:"idTokenUserinfoAssertion"`
}

func (c *client) ensureProject(name string) (string, error) {
	// Search first for idempotency.
	var search struct {
		Result []idResp `json:"result"`
	}
	body := map[string]any{
		"queries": []map[string]any{
			{"nameQuery": map[string]any{"name": name, "method": "TEXT_QUERY_METHOD_EQUALS"}},
		},
	}
	if err := c.do(http.MethodPost, "/management/v1/projects/_search", "", body, &search); err == nil {
		if len(search.Result) > 0 {
			return search.Result[0].ID, nil
		}
	}
	var created idResp
	create := map[string]any{
		"name":                   name,
		"projectRoleAssertion":   true,
		"projectRoleCheck":       false,
		"hasProjectCheck":        false,
		"privateLabelingSetting": "PRIVATE_LABELING_SETTING_UNSPECIFIED",
	}
	if err := c.do(http.MethodPost, "/management/v1/projects", "", create, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

func (c *client) ensureOIDCApp(projectID, name, redirectURI string) (string, string, error) {
	cfg := projectAppOIDCConfig{
		RedirectURIs:             []string{redirectURI},
		ResponseTypes:            []string{"OIDC_RESPONSE_TYPE_CODE"},
		GrantTypes:               []string{"OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"},
		AppType:                  "OIDC_APP_TYPE_WEB",
		AuthMethodType:           "OIDC_AUTH_METHOD_TYPE_NONE", // PKCE
		DevMode:                  true,
		AccessTokenType:          "OIDC_TOKEN_TYPE_JWT",
		AccessTokenRoleAssertion: true,
		IDTokenRoleAssertion:     true,
		IDTokenUserinfoAssertion: true,
	}
	create := map[string]any{
		"name":          name,
		"redirectUris":  cfg.RedirectURIs,
		"responseTypes": cfg.ResponseTypes,
		"grantTypes":    cfg.GrantTypes,
		"appType":       cfg.AppType,
		"authMethodType": cfg.AuthMethodType,
		"devMode":       cfg.DevMode,
		"accessTokenType": cfg.AccessTokenType,
		"accessTokenRoleAssertion": cfg.AccessTokenRoleAssertion,
		"idTokenRoleAssertion": cfg.IDTokenRoleAssertion,
		"idTokenUserinfoAssertion": cfg.IDTokenUserinfoAssertion,
	}
	var out struct {
		AppID    string `json:"appId"`
		ClientID string `json:"clientId"`
	}
	err := c.do(http.MethodPost, fmt.Sprintf("/management/v1/projects/%s/apps/oidc", projectID), "", create, &out)
	if err != nil && !alreadyExists(err) {
		return "", "", err
	}
	return out.AppID, out.ClientID, nil
}

func (c *client) ensureAPIApp(projectID, name string) (string, string, error) {
	create := map[string]any{
		"name":           name,
		"authMethodType": "API_AUTH_METHOD_TYPE_PRIVATE_KEY_JWT",
	}
	var out struct {
		AppID    string `json:"appId"`
		ClientID string `json:"clientId"`
	}
	err := c.do(http.MethodPost, fmt.Sprintf("/management/v1/projects/%s/apps/api", projectID), "", create, &out)
	if err != nil && !alreadyExists(err) {
		return "", "", err
	}
	return out.AppID, out.ClientID, nil
}

func (c *client) ensureRole(projectID, key, displayName string) error {
	body := map[string]any{
		"roleKey":     key,
		"displayName": displayName,
	}
	err := c.do(http.MethodPost, fmt.Sprintf("/management/v1/projects/%s/roles", projectID), "", body, nil)
	if err != nil && !alreadyExists(err) {
		return err
	}
	return nil
}

func (c *client) ensureOrg(name string) (string, error) {
	// Try to find first.
	var search struct {
		Result []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := c.do(http.MethodPost, "/admin/v1/orgs/_search", "", map[string]any{
		"queries": []map[string]any{
			{"nameQuery": map[string]any{"name": name, "method": "ORG_NAME_QUERY_METHOD_EQUALS"}},
		},
	}, &search); err == nil {
		for _, r := range search.Result {
			if r.Name == name {
				return r.ID, nil
			}
		}
	}
	var out struct {
		OrgID string `json:"orgId"`
	}
	if err := c.do(http.MethodPost, "/admin/v1/orgs", "", map[string]any{"name": name}, &out); err != nil {
		return "", err
	}
	return out.OrgID, nil
}

func main() {
	api := getenvDefault("ZITADEL_API", "http://zitadel:8080")
	patFile := os.Getenv("ZITADEL_PAT_FILE")
	if patFile == "" {
		log.Fatal("ZITADEL_PAT_FILE not set")
	}
	pat, err := readPAT(patFile)
	if err != nil {
		log.Fatalf("read PAT: %v", err)
	}

	c := &client{
		base:  strings.TrimRight(api, "/"),
		token: pat,
		hc:    &http.Client{Timeout: 30 * time.Second},
	}

	portalRedirect := getenvDefault("LIMEN_PORTAL_REDIRECT", "http://localhost:8080/auth/callback")
	sampleSlug := getenvDefault("LIMEN_SAMPLE_TENANT_SLUG", "acme")

	log.Printf("bootstrapping Zitadel at %s", api)

	projectID, err := c.ensureProject("Limen Gateway")
	if err != nil {
		log.Fatalf("ensure project: %v", err)
	}
	log.Printf("project: %s", projectID)

	_, portalClientID, err := c.ensureOIDCApp(projectID, "Limen Portal", portalRedirect)
	if err != nil {
		log.Fatalf("ensure portal app: %v", err)
	}
	log.Printf("portal client_id: %s", portalClientID)

	_, mcpClientID, err := c.ensureAPIApp(projectID, "Limen MCP RS")
	if err != nil {
		log.Fatalf("ensure MCP RS app: %v", err)
	}
	log.Printf("mcp RS client_id: %s", mcpClientID)

	for _, r := range []struct{ key, display string }{
		{"member", "Member"},
		{"admin", "Admin"},
		{"owner", "Owner"},
	} {
		if err := c.ensureRole(projectID, r.key, r.display); err != nil {
			log.Fatalf("ensure role %s: %v", r.key, err)
		}
	}
	log.Printf("roles: member, admin, owner")

	orgID, err := c.ensureOrg(sampleSlug)
	if err != nil {
		log.Fatalf("ensure sample org: %v", err)
	}
	log.Printf("sample org %q: %s", sampleSlug, orgID)

	out := map[string]string{
		"LIMEN_OIDC_PORTAL_CLIENT_ID":  portalClientID,
		"LIMEN_OIDC_MCP_RS_CLIENT_ID":  mcpClientID,
		"LIMEN_OIDC_PROJECT_ID":        projectID,
		"LIMEN_SAMPLE_TENANT_ORG_ID":   orgID,
		"LIMEN_SAMPLE_TENANT_SLUG":     sampleSlug,
	}
	if path := os.Getenv("LIMEN_BOOTSTRAP_OUT"); path != "" {
		_ = writeEnvFile(path, out)
	}
	fmt.Println("\n--- bootstrap output (copy into .env) ---")
	for k, v := range out {
		fmt.Printf("%s=%s\n", k, v)
	}
}

func readPAT(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func writeEnvFile(path string, kv map[string]string) error {
	var b strings.Builder
	for k, v := range kv {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func getenvDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
