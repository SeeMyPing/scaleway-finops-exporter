package scaleway

import (
	"context"
	"fmt"

	billingsdk "github.com/scaleway/scaleway-sdk-go/api/billing/v2beta1"
	"github.com/scaleway/scaleway-sdk-go/scw"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/finops"
)

// FinOps implements finops.API with the Scaleway billing v2beta1 FinOps API.
type FinOps struct {
	api *billingsdk.FinOpsAPI
	// pageSize is left to the API default when zero: the API does not document its maximum.
	pageSize uint32
}

var _ finops.API = (*FinOps)(nil)

// NewFinOps returns a FinOps adapter.
func NewFinOps(c *Client) *FinOps {
	return &FinOps{api: billingsdk.NewFinOpsAPI(c.SDK)}
}

// Charges implements finops.API. The FinOps API paginates with an opaque
// token, which scw.WithAllPages does not support, so pages are walked here.
// Charges are clamped to [q.Start, q.End) so that a charge spanning several
// months is only counted for the part inside the period.
func (f *FinOps) Charges(ctx context.Context, q finops.Query) ([]finops.Charge, error) {
	clamp := true
	req := &billingsdk.FinOpsAPIListChargesRequest{
		OrganizationID:   q.OrganizationID,
		ProjectIDs:       q.ProjectIDs,
		StartDateAfter:   &q.Start,
		EndDateBefore:    &q.End,
		ClampToTimeRange: &clamp,
		OrderBy:          billingsdk.ListChargesRequestOrderByStartDateAsc,
	}
	if f.pageSize > 0 {
		req.PageSize = &f.pageSize
	}

	var charges []finops.Charge
	seenTokens := map[string]bool{}
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, fmt.Errorf("list charges: pagination did not end after %d pages", maxPages)
		}
		resp, err := f.api.ListCharges(req, scw.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("list charges: page %d: %w", page, err)
		}
		for _, c := range resp.Charges {
			if c != nil {
				charges = append(charges, charge(c))
			}
		}

		if resp.NextPageToken == nil || *resp.NextPageToken == "" {
			return charges, nil
		}
		token := *resp.NextPageToken
		if seenTokens[token] {
			return nil, fmt.Errorf("list charges: page token repeated at page %d", page)
		}
		seenTokens[token] = true
		req.PageToken = &token
	}
}

func charge(c *billingsdk.Charge) finops.Charge {
	price := money(c.Price)
	out := finops.Charge{
		OrganizationID: c.OrganizationID,
		ProjectID:      c.ProjectID,
		ProjectName:    c.ProjectName,
		SKU:            c.Sku,
		ResourceID:     c.ResourceID,
		Amount:         price.Amount,
		Currency:       price.Currency,
		UpdatedAt:      timeOrZero(c.UpdatedAt),
	}
	if c.ResourceName != nil {
		out.ResourceName = *c.ResourceName
	}
	return out
}
