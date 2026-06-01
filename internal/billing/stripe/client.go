// Package stripe wraps the Stripe Go SDK with Limen's resilience layer.
package stripe

import (
	"context"
	"fmt"
	"sync"

	"github.com/stripe/stripe-go/v82"
	billingportalsession "github.com/stripe/stripe-go/v82/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/customer"
	"github.com/stripe/stripe-go/v82/subscription"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/resilience"
	"github.com/belphemur/limen/internal/valkey"
)

var setupOnce sync.Once

// Client wraps the Stripe Go SDK with Limen's resilience layer.
type Client struct {
	cfg    config.BillingConfig
	logger *zap.Logger
}

// NewClient builds a Stripe client with a resilient HTTP transport.
// stripe-go uses global state; ensure only one Client is constructed per process.
func NewClient(cfg config.BillingConfig, resilienceCfg config.ResiliencePolicy, logger *zap.Logger, valkeyClient valkey.Client) *Client {
	if logger == nil {
		logger = zap.NewNop()
	}
	setupOnce.Do(func() {
		stripe.Key = cfg.Stripe.APIKey
		client := resilience.Client("stripe.api", resilienceCfg, logger, valkeyClient)
		stripe.SetHTTPClient(client)
	})
	return &Client{cfg: cfg, logger: logger}
}

// EnsureCustomer returns an existing Stripe customer ID for the tenant,
// or creates one. tenantPublicID is the tenant's PublicID field used
// as metadata so the Stripe Dashboard is human-readable.
// If existingCustomerID is non-empty and the customer still exists in
// Stripe, it is returned immediately.
func (c *Client) EnsureCustomer(ctx context.Context, tenantPublicID string, existingCustomerID string) (string, error) {
	if existingCustomerID != "" {
		_, err := customer.Get(existingCustomerID, &stripe.CustomerParams{
			Params: stripe.Params{Context: ctx},
		})
		if err == nil {
			return existingCustomerID, nil
		}
		// If deleted in Stripe, fall through to search/create.
	}

	searchParams := &stripe.CustomerSearchParams{
		SearchParams: stripe.SearchParams{
			Context: ctx,
			Query:   fmt.Sprintf("metadata['tenant_public_id']:'%s'", tenantPublicID),
		},
	}
	iter := customer.Search(searchParams)
	for iter.Next() {
		return iter.Customer().ID, nil
	}
	if err := iter.Err(); err != nil {
		return "", fmt.Errorf("stripe: search customers: %w", err)
	}

	newParams := &stripe.CustomerParams{
		Params: stripe.Params{
			Context:  ctx,
			Metadata: map[string]string{"tenant_public_id": tenantPublicID},
		},
	}
	cust, err := customer.New(newParams)
	if err != nil {
		return "", fmt.Errorf("stripe: create customer: %w", err)
	}
	return cust.ID, nil
}

// CreateCheckoutSession creates a Stripe Checkout session for upgrading
// to the Team plan. Uses both line items (active_user + sa_connection)
// with quantities from billing metrics. Sets trial if applicable.
// returnURL is where Stripe redirects after completion.
func (c *Client) CreateCheckoutSession(ctx context.Context, customerID, returnURL string, activeUsers, saConnections int32) (string, error) {
	lineItems := []*stripe.CheckoutSessionLineItemParams{
		{Price: stripe.String(c.cfg.Products.TeamActiveUserPriceID), Quantity: new(int64(activeUsers))},
		{Price: stripe.String(c.cfg.Products.TeamSAConnectionPriceID), Quantity: new(int64(saConnections))},
	}

	params := &stripe.CheckoutSessionParams{
		Params:              stripe.Params{Context: ctx},
		Customer:            stripe.String(customerID),
		Mode:                stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems:           lineItems,
		SuccessURL:          stripe.String(returnURL + "?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:           stripe.String(returnURL + "?canceled=true"),
		AllowPromotionCodes: new(true),
	}

	if c.cfg.TrialDays > 0 {
		hasActive, err := c.hasActiveSubscription(ctx, customerID)
		if err != nil {
			return "", fmt.Errorf("stripe: check active subscription: %w", err)
		}
		if !hasActive {
			params.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{
				TrialPeriodDays: new(int64(c.cfg.TrialDays)),
			}
		}
	}

	sess, err := checkoutsession.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe: create checkout session: %w", err)
	}
	return sess.URL, nil
}

func (c *Client) hasActiveSubscription(ctx context.Context, customerID string) (bool, error) {
	listParams := &stripe.SubscriptionListParams{
		Customer:   stripe.String(customerID),
		Status:     stripe.String(string(stripe.SubscriptionStatusActive)),
		ListParams: stripe.ListParams{Context: ctx},
	}
	iter := subscription.List(listParams)
	for iter.Next() {
		return true, nil
	}
	if err := iter.Err(); err != nil {
		return false, fmt.Errorf("stripe: list subscriptions: %w", err)
	}
	return false, nil
}

// CreatePortalSession creates a Stripe Customer Portal session for
// self-service subscription management.
func (c *Client) CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	params := &stripe.BillingPortalSessionParams{
		Params:    stripe.Params{Context: ctx},
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(returnURL),
	}
	sess, err := billingportalsession.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe: create portal session: %w", err)
	}
	return sess.URL, nil
}
