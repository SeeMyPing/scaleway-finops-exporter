package scaleway

import (
	"context"
	"fmt"
	"time"

	billingsdk "github.com/scaleway/scaleway-sdk-go/api/billing/v2beta1"
	"github.com/scaleway/scaleway-sdk-go/scw"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/billing"
)

const (
	// maxBillingPageSize is the largest page size accepted by the billing API.
	maxBillingPageSize uint32 = 100
	// maxPages guards against an API that never reports the end of a list.
	maxPages = 10_000
)

// Billing implements billing.API with the Scaleway billing v2beta1 API.
type Billing struct {
	api      *billingsdk.API
	pageSize uint32
}

var _ billing.API = (*Billing)(nil)

// NewBilling returns a billing adapter.
func NewBilling(c *Client) *Billing {
	return &Billing{api: billingsdk.NewAPI(c.SDK), pageSize: maxBillingPageSize}
}

// Consumptions implements billing.API.
//
// Pages are walked manually rather than with scw.WithAllPages, because the
// SDK only merges the list itself and drops the other response fields
// (updated_at, total_discount_untaxed_value) when aggregating pages.
func (b *Billing) Consumptions(ctx context.Context, organizationID, period string) (*billing.ConsumptionReport, error) {
	var resp *billingsdk.ListConsumptionsResponse
	err := paginate(func(page int32) (int, uint64, error) {
		r, err := b.api.ListConsumptions(&billingsdk.ListConsumptionsRequest{
			OrganizationID: &organizationID,
			BillingPeriod:  &period,
			Page:           &page,
			PageSize:       &b.pageSize,
		}, scw.WithContext(ctx))
		if err != nil {
			return 0, 0, fmt.Errorf("page %d: %w", page, err)
		}
		if resp == nil {
			resp = r
		} else {
			resp.Consumptions = append(resp.Consumptions, r.Consumptions...)
		}
		return len(r.Consumptions), r.TotalCount, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list consumptions: %w", err)
	}

	report := &billing.ConsumptionReport{
		Consumptions:  make([]billing.Consumption, 0, len(resp.Consumptions)),
		TotalDiscount: resp.TotalDiscountUntaxedValue,
		UpdatedAt:     timeOrZero(resp.UpdatedAt),
	}
	for _, c := range resp.Consumptions {
		if c == nil {
			continue
		}
		report.Consumptions = append(report.Consumptions, billing.Consumption{
			OrganizationID: c.ConsumerID,
			ProjectID:      c.ProjectID,
			ProjectName:    c.ProjectName,
			Category:       c.CategoryName,
			Product:        c.ProductName,
			SKU:            c.Sku,
			Unit:           c.Unit,
			Value:          money(c.Value),
		})
	}
	return report, nil
}

// Taxes implements billing.API.
// Pages are walked manually for the same reason as in Consumptions.
func (b *Billing) Taxes(ctx context.Context, organizationID, period string) (*billing.TaxReport, error) {
	var resp *billingsdk.ListTaxesResponse
	err := paginate(func(page int32) (int, uint64, error) {
		r, err := b.api.ListTaxes(&billingsdk.ListTaxesRequest{
			OrganizationID: organizationID,
			BillingPeriod:  &period,
			Page:           &page,
			PageSize:       &b.pageSize,
		}, scw.WithContext(ctx))
		if err != nil {
			return 0, 0, fmt.Errorf("page %d: %w", page, err)
		}
		if resp == nil {
			resp = r
		} else {
			resp.Taxes = append(resp.Taxes, r.Taxes...)
		}
		return len(r.Taxes), r.TotalCount, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list taxes: %w", err)
	}

	report := &billing.TaxReport{
		Taxes:     make([]billing.Tax, 0, len(resp.Taxes)),
		UpdatedAt: timeOrZero(resp.UpdatedAt),
	}
	for _, t := range resp.Taxes {
		if t == nil {
			continue
		}
		var amount float64
		if t.TotalTaxValue != nil {
			amount = *t.TotalTaxValue
		}
		report.Taxes = append(report.Taxes, billing.Tax{
			Description: t.Description,
			Rate:        t.Rate,
			Value:       billing.Money{Amount: amount, Currency: t.Currency},
		})
	}
	return report, nil
}

// paginate calls fetch for pages 1, 2, ... until the API returns an empty
// page or the number of items reaches the reported total.
func paginate(fetch func(page int32) (items int, total uint64, err error)) error {
	var seen uint64
	for page := int32(1); page <= maxPages; page++ {
		n, total, err := fetch(page)
		if err != nil {
			return err
		}
		seen += uint64(n) //nolint:gosec // n is a slice length, never negative
		if n == 0 || seen >= total {
			return nil
		}
	}
	return fmt.Errorf("pagination did not end after %d pages", maxPages)
}

// money converts an SDK amount. A missing amount is zero with no currency.
func money(m *scw.Money) billing.Money {
	if m == nil {
		return billing.Money{}
	}
	return billing.Money{Amount: m.ToFloat(), Currency: m.CurrencyCode}
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
