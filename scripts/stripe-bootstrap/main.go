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
	"os"
	"strings"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/customer"
	"github.com/stripe/stripe-go/v82/entitlements/feature"
	"github.com/stripe/stripe-go/v82/price"
	"github.com/stripe/stripe-go/v82/product"
	"github.com/stripe/stripe-go/v82/productfeature"
	"github.com/stripe/stripe-go/v82/subscription"
	"github.com/stripe/stripe-go/v82/webhookendpoint"
	"go.uber.org/zap"
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

// DevSubscriptionInfo holds the Stripe IDs for a dev subscription.
type DevSubscriptionInfo struct {
	CustomerID     string
	SubscriptionID string
}

// bootstrap holds the Stripe client configuration.
type bootstrap struct{}

const (
	managedResourceMetadataKey   = "limen_managed"
	managedResourceMetadataValue = "true"
)

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
			if p.Metadata[managedResourceMetadataKey] != managedResourceMetadataValue {
				continue
			}
			// Orphan — archive it.
			if p.Active {
				_, err := product.Update(p.ID, &stripe.ProductParams{
					Active: stripe.Bool(false),
				})
				if err != nil {
					return nil, fmt.Errorf("archive orphan product %q (%s): %w", p.Name, p.ID, err)
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
		if p.Metadata[managedResourceMetadataKey] != managedResourceMetadataValue {
			needsUpdate = true
			updateParams.AddMetadata(managedResourceMetadataKey, managedResourceMetadataValue)
		}
		if needsUpdate {
			if _, err := product.Update(p.ID, updateParams); err != nil {
				return nil, fmt.Errorf("update product %q: %w", d.Key, err)
			}
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
		params := &stripe.ProductParams{
			Name:        stripe.String(d.Name),
			Description: stripe.String(d.Description),
			Active:      stripe.Bool(true),
		}
		params.AddMetadata(managedResourceMetadataKey, managedResourceMetadataValue)
		p, err := product.New(params)
		if err != nil {
			return nil, fmt.Errorf("create product %q: %w", d.Key, err)
		}
		found[d.Key] = p.ID
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

	for iter.Next() {
		pr := iter.Price()
		if pr == nil || pr.LookupKey == "" {
			continue
		}
		if d, ok := desiredKeys[pr.LookupKey]; ok {
			found[d.Key] = pr.ID
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list prices: %w", err)
	}

	// Create missing prices.
	for _, d := range desired {
		if _, ok := found[d.Key]; ok {
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
	}

	return found, nil
}

// ensureFeatures searches by lookup_key, creates if missing, and returns
// map[lookupKey]stripeID.
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
			}
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

// ensureDevCustomer returns an existing Stripe customer matching the dev
// label, or creates one. Idempotent: re-running is safe.
func (b *bootstrap) ensureDevCustomer(label string) (string, error) {
	// Search for existing customer by metadata.
	searchParams := &stripe.CustomerSearchParams{
		SearchParams: stripe.SearchParams{
			Query: fmt.Sprintf("metadata['limen_dev_tenant']:'%s'", label),
		},
	}
	iter := customer.Search(searchParams)
	for iter.Next() {
		return iter.Customer().ID, nil
	}
	if err := iter.Err(); err != nil {
		return "", fmt.Errorf("search dev customer: %w", err)
	}

	// Create new customer.
	params := &stripe.CustomerParams{
		Params: stripe.Params{
			Metadata: map[string]string{
				"limen_dev_tenant":         label,
				managedResourceMetadataKey: managedResourceMetadataValue,
			},
		},
	}
	cust, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("create dev customer: %w", err)
	}
	return cust.ID, nil
}

// ensureDevSubscription returns an existing active subscription for the
// given customer, or creates a Team-plan subscription. Idempotent.
func (b *bootstrap) ensureDevSubscription(customerID, label string, priceIDs map[string]string) (string, error) {
	// Check for existing active subscription (idempotency).
	listParams := &stripe.SubscriptionListParams{
		Customer: stripe.String(customerID),
		Status:   stripe.String("active"),
	}
	listIter := subscription.List(listParams)
	for listIter.Next() {
		sub := listIter.Subscription()
		// Verify this is our dev subscription, not an unrelated one.
		if sub.Metadata["limen_dev_tenant"] == label {
			return sub.ID, nil
		}
	}
	if err := listIter.Err(); err != nil {
		return "", fmt.Errorf("list dev subscriptions: %w", err)
	}

	// Create new subscription with Team prices.
	params := &stripe.SubscriptionParams{
		Customer: stripe.String(customerID),
		Items: []*stripe.SubscriptionItemsParams{
			{Price: stripe.String(priceIDs["team_active_user"]), Quantity: stripe.Int64(1)},
			{Price: stripe.String(priceIDs["team_sa_connection"]), Quantity: stripe.Int64(1)},
		},
		Metadata: map[string]string{
			"limen_dev_tenant":         label,
			managedResourceMetadataKey: managedResourceMetadataValue,
		},
		PaymentBehavior:   stripe.String("default_incomplete"),
		ProrationBehavior: stripe.String("none"),
	}
	sub, err := subscription.New(params)
	if err != nil {
		return "", fmt.Errorf("create dev subscription: %w", err)
	}
	return sub.ID, nil
}

var outputKeys = []string{
	"STRIPE_DEVELOPER_PRODUCT_ID",
	"STRIPE_TEAM_PRODUCT_ID",
	"STRIPE_DEV_TRACKING_PRICE_ID",
	"STRIPE_TEAM_ACTIVE_USER_PRICE_ID",
	"STRIPE_TEAM_SA_CONNECTION_PRICE_ID",
	"STRIPE_WEBHOOK_SECRET",
	"STRIPE_DEV_TENANT_CUSTOMER_ID",
	"STRIPE_DEV_TENANT_SUBSCRIPTION_ID",
}

func main() {
	// stripe-go uses its own HTTP client; no context plumbing needed for bootstrap.
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = logger.Sync()
	}()

	apiKey := os.Getenv("STRIPE_API_KEY")
	if apiKey == "" {
		logger.Fatal("STRIPE_API_KEY not set")
	}
	if strings.HasPrefix(apiKey, "sk_live_") && os.Getenv("STRIPE_BOOTSTRAP_ALLOW_LIVE") != "1" {
		logger.Fatal("refusing live Stripe key; set STRIPE_BOOTSTRAP_ALLOW_LIVE=1 to override intentionally")
	}
	stripe.Key = apiKey

	b := &bootstrap{}
	var devInfo DevSubscriptionInfo

	// --- Products ---
	desiredProducts := []DesiredProduct{
		{Key: "developer", Name: "Limen Developer", Description: "Free developer-tier product with hard limits. Includes nominal tracking price."},
		{Key: "team", Name: "Limen Team", Description: "Paid team-tier product. Billed per active user + per concurrent SA connection."},
	}
	productIDs, err := b.ensureProducts(desiredProducts)
	if err != nil {
		logger.Fatal("ensure products", zap.Error(err))
	}
	for k, id := range productIDs {
		logger.Info("product ensured", zap.String("key", k), zap.String("id", id))
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
		logger.Fatal("ensure prices", zap.Error(err))
	}
	for k, id := range priceIDs {
		logger.Info("price ensured", zap.String("key", k), zap.String("id", id))
	}

	// --- Dev Subscription (optional, gated by LIMEN_DEV_TENANT_LABEL) ---
	devLabel := os.Getenv("LIMEN_DEV_TENANT_LABEL")
	if devLabel != "" {
		customerID, err := b.ensureDevCustomer(devLabel)
		if err != nil {
			logger.Fatal("ensure dev customer", zap.Error(err))
		}
		logger.Info("dev customer ensured", zap.String("customer_id", customerID))

		subscriptionID, err := b.ensureDevSubscription(customerID, devLabel, priceIDs)
		if err != nil {
			logger.Fatal("ensure dev subscription", zap.Error(err))
		}
		logger.Info("dev subscription ensured", zap.String("subscription_id", subscriptionID))

		devInfo = DevSubscriptionInfo{
			CustomerID:     customerID,
			SubscriptionID: subscriptionID,
		}
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
		logger.Fatal("ensure features", zap.Error(err))
	}
	for k, id := range featureIDs {
		logger.Info("feature ensured", zap.String("key", k), zap.String("id", id))
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
		logger.Fatal("ensure product features", zap.Error(err))
	}
	logger.Info("product-feature attachments ensured")

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
			logger.Fatal("ensure webhook endpoints", zap.Error(err))
		}
		webhookInfo = webhookMap[webhookURL]
		if webhookInfo.Secret == "" {
			if envSecret := os.Getenv("STRIPE_WEBHOOK_SECRET"); envSecret != "" {
				webhookInfo.Secret = envSecret
				logger.Warn("webhook secret not returned by Stripe; using STRIPE_WEBHOOK_SECRET from environment", zap.String("url", webhookURL))
			} else {
				logger.Fatal("webhook secret not returned by Stripe for existing endpoint; set STRIPE_WEBHOOK_SECRET to preserve idempotent output", zap.String("url", webhookURL))
			}
		}
		logger.Info("webhook endpoint ensured", zap.String("id", webhookInfo.ID), zap.String("url", webhookURL))
	} else {
		logger.Info("STRIPE_WEBHOOK_URL not set, skipping webhook endpoint")
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
	if devInfo.CustomerID != "" {
		out["STRIPE_DEV_TENANT_CUSTOMER_ID"] = devInfo.CustomerID
		out["STRIPE_DEV_TENANT_SUBSCRIPTION_ID"] = devInfo.SubscriptionID
	}

	outPath := os.Getenv("LIMEN_BOOTSTRAP_OUT")
	if outPath == "" {
		outPath = ".bootstrap-out.env"
	}
	if err := writeEnvFile(outPath, out); err != nil {
		logger.Fatal("write output env file", zap.String("path", outPath), zap.Error(err))
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
