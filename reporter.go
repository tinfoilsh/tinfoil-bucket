package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	usagereporting "github.com/tinfoilsh/usage-reporting-go"
	usageclient "github.com/tinfoilsh/usage-reporting-go/client"
)

type Reporter struct {
	client *usageclient.ReporterClient
}

// NewReporter returns a usage reporter that batches and signs operation events
// to the controlplane. If controlPlaneURL or secret is empty the reporter is a
// silent no-op so local development without controlplane wiring keeps working.
func NewReporter(controlPlaneURL, reporterID, secret string) (*Reporter, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(controlPlaneURL), "/")
	if endpoint == "" || secret == "" {
		return &Reporter{client: usageclient.New(usageclient.Config{})}, nil
	}
	endpoint += usagereporting.IngestionPath
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	return &Reporter{
		client: usageclient.New(usageclient.Config{
			Endpoint:   endpoint,
			ReporterID: reporterID,
			Secret:     secret,
		}),
	}, nil
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid usage reporter endpoint %q: %w", endpoint, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("usage reporter endpoint %q must use https scheme", endpoint)
	}
	if parsed.Host == "" {
		return fmt.Errorf("usage reporter endpoint %q is missing a host", endpoint)
	}
	return nil
}

// ReportOperation emits one usage event. CustomerRequests=1 + Operation.Class
// drive flat per-class pricing; Operation.Name is the granular audit label.
// Storage-at-rest is billed by S3 directly, so we don't ship byte meters.
// apiKey is the owner-attribution key — the controlplane resolves it to a
// user_id on its side, so we don't need to ship that on the event.
func (r *Reporter) ReportOperation(apiKey, operationName, class string) {
	if r == nil || r.client == nil || !r.client.Enabled() {
		return
	}
	if apiKey == "" {
		return
	}
	r.client.AddEvent(usagereporting.Event{
		OccurredAt: time.Now().UTC(),
		APIKey:     apiKey,
		Operation: usagereporting.Operation{
			Service: usagereporting.ServiceBuckets,
			Name:    operationName,
			Class:   class,
		},
		CustomerRequests: 1,
	})
}

func (r *Reporter) Close(ctx context.Context) {
	if r == nil || r.client == nil {
		return
	}
	r.client.Stop(ctx)
}
