package scaleway

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	efsdk "github.com/scaleway/scaleway-sdk-go/api/environmental_footprint/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"

	"github.com/SeeMyPing/scaleway-finops-exporter/internal/source/footprint"
)

// Footprint implements footprint.API with the Environmental Footprint v1alpha1 API.
type Footprint struct {
	api *efsdk.UserAPI
}

var _ footprint.API = (*Footprint)(nil)

// NewFootprint returns a footprint adapter.
func NewFootprint(c *Client) *Footprint {
	return &Footprint{api: efsdk.NewUserAPI(c.SDK)}
}

// ImpactData implements footprint.API. It flattens the project > region >
// (zone >) SKU tree into rows: SKUs attached directly to a region are
// regional products and get an empty zone.
func (f *Footprint) ImpactData(ctx context.Context, q footprint.Query) (*footprint.Report, error) {
	start, end := q.Start, q.End
	resp, err := f.api.GetImpactData(&efsdk.UserAPIGetImpactDataRequest{
		OrganizationID: q.OrganizationID,
		StartDate:      &start,
		EndDate:        &end,
		ProjectIDs:     q.ProjectIDs,
	}, scw.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("get impact data: %w", err)
	}

	report := &footprint.Report{
		ProjectCarbonGrams: map[string]float64{},
		Start:              timeOrZero(resp.StartDate),
		End:                timeOrZero(resp.EndDate),
	}
	for _, p := range resp.Projects {
		if p == nil {
			continue
		}
		if p.TotalProjectImpact != nil {
			report.ProjectCarbonGrams[p.ProjectID] = kgToGrams(p.TotalProjectImpact.KgCo2Equivalent)
		}
		for _, r := range p.Regions {
			if r == nil {
				continue
			}
			report.Rows = appendSKUs(report.Rows, p.ProjectID, r.Region.String(), "", r.Skus)
			for _, z := range r.Zones {
				if z != nil {
					report.Rows = appendSKUs(report.Rows, p.ProjectID, r.Region.String(), z.Zone.String(), z.Skus)
				}
			}
		}
	}
	return report, nil
}

func appendSKUs(rows []footprint.Row, project, region, zone string, skus []*efsdk.SkuImpact) []footprint.Row {
	for _, s := range skus {
		if s == nil || s.TotalSkuImpact == nil {
			continue
		}
		rows = append(rows, footprint.Row{
			ProjectID:        project,
			Region:           region,
			Zone:             zone,
			ServiceCategory:  string(s.ServiceCategory),
			ProductCategory:  string(s.ProductCategory),
			CarbonGrams:      kgToGrams(s.TotalSkuImpact.KgCo2Equivalent),
			WaterCubicMeters: scaledFloat32(s.TotalSkuImpact.M3WaterUsage, 0),
		})
	}
	return rows
}

func kgToGrams(kg float32) float64 { return scaledFloat32(kg, 3) }

// scaledFloat32 returns v * 10^pow10 as the float64 closest to the shortest
// decimal representation of v. A plain float64(v) would expose float32
// artifacts (0.123 would become 0.12300000339746475), and multiplying by 1000
// afterwards would add float64 rounding errors on top.
func scaledFloat32(v float32, pow10 int) float64 {
	s := strconv.FormatFloat(float64(v), 'e', -1, 32) // for example "1.23e-01"
	mantissa, exp, ok := strings.Cut(s, "e")
	if !ok {
		return float64(v) // NaN or Inf
	}
	e, err := strconv.Atoi(exp)
	if err != nil {
		return float64(v)
	}
	out, err := strconv.ParseFloat(mantissa+"e"+strconv.Itoa(e+pow10), 64)
	if err != nil {
		return float64(v)
	}
	return out
}
