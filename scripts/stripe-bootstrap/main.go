package main

// Package main implements the Stripe billing bootstrap for Limen dev environments.
//
// It is idempotent: re-running it is safe. It ensures the Limen product
// catalog (Products, Prices, Features, and Product-Feature attachments) and a
// webhook endpoint exist in the configured Stripe account. All work is done
// through the official stripe-go/v82 SDK.
//
// Authentication: STRIPE_API_KEY env var (required).
// Output: writes IDs to .bootstrap-out.env (or LIMEN_BOOTSTRAP_OUT).

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/entitlements/feature"
	"github.com/stripe/stripe-go/v82/price"
	"github.com/stripe/stripe-go/v82/product"
	"github.com/stripe/stripe-go/v82/productfeature"
	"github.com/stripe/stripe-go/v82/webhookendpoint"
)

// DesiredProduct defines a product that must exist in Stripe.
type DesiredProduct struct {
	Key         string
	Name        string
	Description string
}

// DesiredPrice defines a price that must exist in Stripe.
type DesiredPrice struct {
	Key        string
	ProductKey string
	UnitAmount int64
	Currency   string
	Interval   string
	UsageType  string
	LookupKey  string
	Nickname   string
}

// DesiredFeature defines an entitlements feature that must exist in Stripe.
type DesiredFeature struct {
	LookupKey string
	Name      string
}

// DesiredProductFeature defines a feature attachment to a product.
type DesiredProductFeature struct {
	ProductKey string
	FeatureKey string // lookup_key of the feature
}

// DesiredWebhookEndpoint defines a webhook endpoint that must exist in Stripe.
type DesiredWebhookEndpoint struct {
	URL         string
	Events      []string
	Description string
}

// WebhookInfo holds the ID and secret for a webhook endpoint.
type WebhookInfo struct {
	ID     string
	Secret string
}

// bootstrap holds the Stripe client configuration.
type bootstrap struct{}

// ensureProducts lists all products, converges with desired state
// (create/update/archive), and returns map[productKey]stripeID.
func (b *bootstrap) ensureProducts(desired []DesiredProduct) (map[string]string, error) {
	desiredByName := make(map[string]DesiredProduct, len(desired))
	for _, d := range desired {
		desiredByName[d.Name] = d
	}

	found := make(map[string]string, len(desired))

	// List all products (active and inactive) to find orphans.
	params := &stripe.ProductListParams{}
	params.Limit = stripe.Int64(100)
	iter := product.List(params)
	for iter.Next() {
		p := iter.Product()
		if p == nil {
			continue
		}
		d, ok := desiredByName[p.Name]
		if !ok {
			// Orphan — archive it.
			if p.Active {
				log.Printf("warning: archiving orphan product %q (%s)", p.Name, p.ID)
				_, err := product.Update(p.ID, &stripe.ProductParams{
					Active: stripe.Bool(false),
				})
				if err != nil {
					log.Printf("warning: failed to archive product %s: %v", p.ID, err)
				}
			}
			continue
		}
		// Desired product found — ensure active and description.
		found[d.Key] = p.ID
		needsUpdate := false
		updateParams := &stripe.ProductParams{}
		if !p.Active {
			needsUpdate = true
			updateParams.Active = stripe.Bool(true)
		}
		if p.Description != d.Description {
			needsUpdate = true
			updateParams.Description = stripe.String(d.Description)
		}
		if needsUpdate {
			if _, err := product.Update(p.ID, updateParams); err != nil {
				return nil, fmt.Errorf("update product %q: %w", d.Key, err)
			}
			log.Printf("updated product %q (%s)", d.Key, p.ID)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}

	// Create missing products.
	for _, d := range desired {
		if _, ok := found[d.Key]; ok {
			continue
		}
		p, err := product.New(&stripe.ProductParams{
			Name:        stripe.String(d.Name),
			Description: stripe.String(d.Description),
			Active:      stripe.Bool(true),
		})
		if err != nil {
			return nil, fmt.Errorf("create product %q: %w", d.Key, err)
		}
		found[d.Key] = p.ID
		log.Printf("created product %q (%s)", d.Key, p.ID)
	}

	return found, nil
}

// ensurePrices lists all prices (read-after-write consistent), filters by
// lookup_key in Go, creates missing prices, and returns map[priceKey]stripeID.
//
// Prices are immutable in Stripe. The bootstrap creates missing prices
// but cannot update existing ones. To change a price, remove it from
// the desired state, archive it manually in the Stripe Dashboard, and
// add a new entry with a different lookup_key.
func (b *bootstrap) ensurePrices(desired []DesiredPrice, productIDs map[string]string) (map[string]string, error) {
	found := make(map[string]string, len(desired))

	// Build lookup set from desired prices.
	desiredKeys := make(map[string]DesiredPrice, len(desired))
	for _, d := range desired {
		desiredKeys[d.LookupKey] = d
	}

	// List all prices (read-after-write consistent, unlike Search).
	params := &stripe.PriceListParams{}
	params.Limit = stripe.Int64(100)
	iter := price.List(params)

	// Collect all prices to detect orphaned ones later.
	var allPrices []*stripe.Price
	for iter.Next() {
		pr := iter.Price()
		if pr == nil || pr.LookupKey == "" {
			continue
		}
		allPrices = append(allPrices, pr)
		if d, ok := desiredKeys[pr.LookupKey]; ok {
			found[d.Key] = pr.ID
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list prices: %w", err)
	}

	// Log orphan prices (exist in Stripe but not in desired set).
	for _, pr := range allPrices {
		if _, ok := desiredKeys[pr.LookupKey]; !ok {
			log.Printf("warning: orphaned price %q (%s) exists in Stripe but is not desired", pr.LookupKey, pr.ID)
		}
	}

	// Create missing prices.
	for _, d := range desired {
		if _, ok := found[d.Key]; ok {
			log.Printf("found price %q (%s)", d.Key, d.LookupKey)
			continue
		}

		productID, ok := productIDs[d.ProductKey]
		if !ok {
			return nil, fmt.Errorf("price %q references unknown product %q", d.Key, d.ProductKey)
		}

		params := &stripe.PriceParams{
			Product:    stripe.String(productID),
			UnitAmount: stripe.Int64(d.UnitAmount),
			Currency:   stripe.String(d.Currency),
			Recurring: &stripe.PriceRecurringParams{
				Interval:  stripe.String(d.Interval),
				UsageType: stripe.String(d.UsageType),
			},
			LookupKey: stripe.String(d.LookupKey),
			Nickname:  stripe.String(d.Nickname),
		}
		pr, err := price.New(params)
		if err != nil {
			return nil, fmt.Errorf("create price %q: %w", d.Key, err)
		}
		found[d.Key] = pr.ID
		log.Printf("created price %q (%s)", d.Key, pr.ID)
	}

	return found, nil
}

// ensureFeatures searches by lookup_key, creates if missing, logs orphaned
// features, and returns map[lookupKey]stripeID.
func (b *bootstrap) ensureFeatures(desired []DesiredFeature) (map[string]string, error) {
	desiredByKey := make(map[string]DesiredFeature, len(desired))
	for _, d := range desired {
		desiredByKey[d.LookupKey] = d
	}

	found := make(map[string]string, len(desired))

	// List all features.
	params := &stripe.EntitlementsFeatureListParams{}
	params.Limit = stripe.Int64(100)
	iter := feature.List(params)
	for iter.Next() {
		f := iter.EntitlementsFeature()
		if f == nil {
			continue
		}
		if d, ok := desiredByKey[f.LookupKey]; ok {
			found[f.LookupKey] = f.ID
			// Check if name matches desired.
			if f.Name != d.Name {
				_, err := feature.Update(f.ID, &stripe.EntitlementsFeatureParams{
					Name: stripe.String(d.Name),
				})
				if err != nil {
					return nil, fmt.Errorf("update feature %q name: %w", f.LookupKey, err)
				}
				log.Printf("updated feature %q name to %q", f.LookupKey, d.Name)
			}
		} else {
			log.Printf("warning: orphaned feature %q (%s) exists in Stripe but is not desired", f.LookupKey, f.ID)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list features: %w", err)
	}

	// Create missing features.
	for _, d := range desired {
		if _, ok := found[d.LookupKey]; ok {
			continue
		}
		f, err := feature.New(&stripe.EntitlementsFeatureParams{
			Name:      stripe.String(d.Name),
			LookupKey: stripe.String(d.LookupKey),
		})
		if err != nil {
			return nil, fmt.Errorf("create feature %q: %w", d.LookupKey, err)
		}
		found[d.LookupKey] = f.ID
		log.Printf("created feature %q (%s)", d.LookupKey, f.ID)
	}

	return found, nil
}

// ensureProductFeatures checks that each DesiredProductFeature attachment
// exists, and attaches it if missing.
func (b *bootstrap) ensureProductFeatures(desired []DesiredProductFeature, productIDs map[string]string, featureIDs map[string]string) error {
	// Group desired attachments by product.
	byProduct := make(map[string][]string) // productKey -> []featureLookupKey
	for _, d := range desired {
		byProduct[d.ProductKey] = append(byProduct[d.ProductKey], d.FeatureKey)
	}

	for productKey, featureKeys := range byProduct {
		productID, ok := productIDs[productKey]
		if !ok {
			return fmt.Errorf("product feature references unknown product %q", productKey)
		}

		// List existing attachments for this product.
		attached := make(map[string]struct{})
		params := &stripe.ProductFeatureListParams{}
		params.Product = stripe.String(productID)
		params.Limit = stripe.Int64(100)
		iter := productfeature.List(params)
		for iter.Next() {
			pf := iter.ProductFeature()
			if pf != nil && pf.EntitlementFeature != nil {
				attached[pf.EntitlementFeature.ID] = struct{}{}
			}
		}
		if err := iter.Err(); err != nil {
			return fmt.Errorf("list product features for product %q: %w", productKey, err)
		}

		// Attach missing features.
		for _, fk := range featureKeys {
			featureID, ok := featureIDs[fk]
			if !ok {
				return fmt.Errorf("product %q references unknown feature %q", productKey, fk)
			}
			if _, ok := attached[featureID]; ok {
				continue
			}
			_, err := productfeature.New(&stripe.ProductFeatureParams{
				Product:            stripe.String(productID),
				EntitlementFeature: stripe.String(featureID),
			})
			if err != nil {
				return fmt.Errorf("attach feature %q to product %q: %w", fk, productKey, err)
			}
			log.Printf("attached feature %q to product %q", fk, productKey)
		}
	}

	return nil
}

// ensureWebhookEndpoints searches by URL, updates events if found, creates
// if missing, and returns map[url]WebhookInfo.
func (b *bootstrap) ensureWebhookEndpoints(desired []DesiredWebhookEndpoint) (map[string]WebhookInfo, error) {
	found := make(map[string]WebhookInfo, len(desired))

	// List all webhook endpoints.
	existingByURL := make(map[string]*stripe.WebhookEndpoint)
	params := &stripe.WebhookEndpointListParams{}
	params.Limit = stripe.Int64(100)
	iter := webhookendpoint.List(params)
	for iter.Next() {
		we := iter.WebhookEndpoint()
		if we != nil {
			existingByURL[we.URL] = we
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list webhook endpoints: %w", err)
	}

	for _, d := range desired {
		existing, ok := existingByURL[d.URL]
		if ok {
			// Check if events need updating.
			if !eventsEqual(existing.EnabledEvents, d.Events) {
				_, err := webhookendpoint.Update(existing.ID, &stripe.WebhookEndpointParams{
					EnabledEvents: enabledEventPointers(d.Events),
				})
				if err != nil {
					return nil, fmt.Errorf("update webhook endpoint %q: %w", d.URL, err)
				}
				log.Printf("updated webhook endpoint %q events", d.URL)
			}
			found[d.URL] = WebhookInfo{ID: existing.ID, Secret: existing.Secret}
			continue
		}

		// Create new webhook endpoint.
		we, err := webhookendpoint.New(&stripe.WebhookEndpointParams{
			URL:           stripe.String(d.URL),
			EnabledEvents: enabledEventPointers(d.Events),
			Description:   stripe.String(d.Description),
		})
		if err != nil {
			return nil, fmt.Errorf("create webhook endpoint %q: %w", d.URL, err)
		}
		found[d.URL] = WebhookInfo{ID: we.ID, Secret: we.Secret}
		log.Printf("created webhook endpoint %q (%s)", d.URL, we.ID)
	}

	return found, nil
}

// eventsEqual reports whether two event slices contain the same elements
// regardless of order.
func eventsEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, e := range a {
		set[e] = struct{}{}
	}
	for _, e := range b {
		if _, ok := set[e]; !ok {
			return false
		}
	}
	return true
}

var outputKeys = []string{
	"STRIPE_DEVELOPER_PRODUCT_ID",
	"STRIPE_TEAM_PRODUCT_ID",
	"STRIPE_DEV_TRACKING_PRICE_ID",
	"STRIPE_TEAM_ACTIVE_USER_PRICE_ID",
	"STRIPE_TEAM_SA_CONNECTION_PRICE_ID",
	"STRIPE_WEBHOOK_SECRET",
}

func main() {
	// stripe-go uses its own HTTP client; no context plumbing needed for bootstrap.

	apiKey := os.Getenv("STRIPE_API_KEY")
	if apiKey == "" {
		log.Fatal("STRIPE_API_KEY not set")
	}
	stripe.Key = apiKey

	b := &bootstrap{}

	// --- Products ---
	desiredProducts := []DesiredProduct{
		{Key: "developer", Name: "Limen Developer", Description: "Free developer-tier product with hard limits. Includes nominal tracking price."},
		{Key: "team", Name: "Limen Team", Description: "Paid team-tier product. Billed per active user + per concurrent SA connection."},
	}
	productIDs, err := b.ensureProducts(desiredProducts)
	if err != nil {
		log.Fatalf("ensure products: %v", err)
	}
	for k, id := range productIDs {
		log.Printf("product %q: %s", k, id)
	}

	// --- Prices ---
	desiredPrices := []DesiredPrice{
		{
			Key:        "dev_tracking",
			ProductKey: "developer",
			UnitAmount: 1,
			Currency:   "usd",
			Interval:   "month",
			UsageType:  "licensed",
			LookupKey:  "limen_developer_monthly",
			Nickname:   "Developer Plan (Monthly tracking)",
		},
		{
			Key:        "team_active_user",
			ProductKey: "team",
			UnitAmount: 0,
			Currency:   "usd",
			Interval:   "month",
			UsageType:  "licensed",
			LookupKey:  "limen_team_per_active_user",
			Nickname:   "Per Active User",
		},
		{
			Key:        "team_sa_connection",
			ProductKey: "team",
			UnitAmount: 0,
			Currency:   "usd",
			Interval:   "month",
			UsageType:  "licensed",
			LookupKey:  "limen_team_per_sa_connection",
			Nickname:   "Per SA Connection",
		},
	}
	priceIDs, err := b.ensurePrices(desiredPrices, productIDs)
	if err != nil {
		log.Fatalf("ensure prices: %v", err)
	}
	for k, id := range priceIDs {
		log.Printf("price %q: %s", k, id)
	}

	// --- Features ---
	desiredFeatures := []DesiredFeature{
		{LookupKey: "max-user_1", Name: "Maximum 1 Active User"},
		{LookupKey: "max-user_unlimited", Name: "Unlimited Active Users"},
		{LookupKey: "max-service-account_1", Name: "Maximum 1 Service Account"},
		{LookupKey: "max-service-account_unlimited", Name: "Unlimited Service Accounts"},
		{LookupKey: "max-sa-connection_1", Name: "Maximum 1 SA Connection"},
		{LookupKey: "max-sa-connection_unlimited", Name: "Unlimited SA Connections"},
		{LookupKey: "max-upstream-link_5", Name: "Maximum 5 Upstream Links"},
		{LookupKey: "max-upstream-link_unlimited", Name: "Unlimited Upstream Links"},
		{LookupKey: "audit-retention_7d", Name: "7-Day Audit Retention"},
		{LookupKey: "audit-retention_90d", Name: "90-Day Audit Retention"},
		{LookupKey: "sso", Name: "Single Sign-On (SSO)"},
		{LookupKey: "code-mode", Name: "Code-Mode Editing"},
		{LookupKey: "custom-upstream", Name: "Custom Upstream Support"},
		{LookupKey: "ide-preset", Name: "IDE Presets"},
	}
	featureIDs, err := b.ensureFeatures(desiredFeatures)
	if err != nil {
		log.Fatalf("ensure features: %v", err)
	}
	for k, id := range featureIDs {
		log.Printf("feature %q: %s", k, id)
	}

	// --- Product-Feature Attachments ---
	desiredProductFeatures := []DesiredProductFeature{
		// Developer
		{ProductKey: "developer", FeatureKey: "max-user_1"},
		{ProductKey: "developer", FeatureKey: "max-service-account_1"},
		{ProductKey: "developer", FeatureKey: "max-sa-connection_1"},
		{ProductKey: "developer", FeatureKey: "max-upstream-link_5"},
		{ProductKey: "developer", FeatureKey: "audit-retention_7d"},
		{ProductKey: "developer", FeatureKey: "code-mode"},
		{ProductKey: "developer", FeatureKey: "custom-upstream"},
		{ProductKey: "developer", FeatureKey: "ide-preset"},
		// Team
		{ProductKey: "team", FeatureKey: "max-user_unlimited"},
		{ProductKey: "team", FeatureKey: "max-service-account_unlimited"},
		{ProductKey: "team", FeatureKey: "max-sa-connection_unlimited"},
		{ProductKey: "team", FeatureKey: "max-upstream-link_unlimited"},
		{ProductKey: "team", FeatureKey: "audit-retention_90d"},
		{ProductKey: "team", FeatureKey: "sso"},
		{ProductKey: "team", FeatureKey: "code-mode"},
		{ProductKey: "team", FeatureKey: "custom-upstream"},
		{ProductKey: "team", FeatureKey: "ide-preset"},
	}
	if err := b.ensureProductFeatures(desiredProductFeatures, productIDs, featureIDs); err != nil {
		log.Fatalf("ensure product features: %v", err)
	}
	log.Printf("product-feature attachments ensured")

	// --- Webhook Endpoint ---
	webhookURL := os.Getenv("STRIPE_WEBHOOK_URL")
	var webhookInfo WebhookInfo
	if webhookURL != "" {
		desiredWebhooks := []DesiredWebhookEndpoint{
			{
				URL: webhookURL,
				Events: []string{
					"checkout.session.completed",
					"customer.subscription.updated",
					"customer.subscription.deleted",
					"invoice.payment_failed",
					"invoice.payment_succeeded",
					"entitlements.active_entitlement_summary.updated",
				},
				Description: "Limen billing webhook",
			},
		}
		webhookMap, err := b.ensureWebhookEndpoints(desiredWebhooks)
		if err != nil {
			log.Fatalf("ensure webhook endpoints: %v", err)
		}
		webhookInfo = webhookMap[webhookURL]
		log.Printf("webhook endpoint: %s", webhookInfo.ID)
	} else {
		log.Printf("STRIPE_WEBHOOK_URL not set, skipping webhook endpoint")
	}

	// --- Output ---
	out := map[string]string{
		"STRIPE_DEVELOPER_PRODUCT_ID":        productIDs["developer"],
		"STRIPE_TEAM_PRODUCT_ID":             productIDs["team"],
		"STRIPE_DEV_TRACKING_PRICE_ID":       priceIDs["dev_tracking"],
		"STRIPE_TEAM_ACTIVE_USER_PRICE_ID":   priceIDs["team_active_user"],
		"STRIPE_TEAM_SA_CONNECTION_PRICE_ID": priceIDs["team_sa_connection"],
	}
	if webhookInfo.Secret != "" {
		out["STRIPE_WEBHOOK_SECRET"] = webhookInfo.Secret
	}

	outPath := os.Getenv("LIMEN_BOOTSTRAP_OUT")
	if outPath == "" {
		outPath = ".bootstrap-out.env"
	}
	if err := writeEnvFile(outPath, out); err != nil {
		log.Fatalf("write %s: %v", outPath, err)
	}

	fmt.Println("\n--- bootstrap output (copy into .env) ---")
	for _, k := range outputKeys {
		if k == "STRIPE_WEBHOOK_SECRET" {
			continue // don't print secret to stdout
		}
		if v, ok := out[k]; ok {
			fmt.Printf("%s=%s\n", k, v)
		}
	}
	// Print a note about the webhook secret
	if out["STRIPE_WEBHOOK_SECRET"] != "" {
		fmt.Println("STRIPE_WEBHOOK_SECRET=saved to .bootstrap-out.env")
	}
}

func enabledEventPointers(events []string) []*string {
	out := make([]*string, len(events))
	for i, e := range events {
		out[i] = stripe.String(e)
	}
	return out
}

func writeEnvFile(path string, kv map[string]string) error {
	var b strings.Builder
	for _, k := range outputKeys {
		if v, ok := kv[k]; ok {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
