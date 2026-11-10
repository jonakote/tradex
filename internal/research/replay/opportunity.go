package replay

import (
	"time"

	"tradex/internal/domain"
	"tradex/internal/risk"
)

type OpportunityStatus string

const (
	OpportunityUnconfirmed    OpportunityStatus = "UNCONFIRMED"
	OpportunityExpired        OpportunityStatus = "EXPIRED"
	OpportunityRiskRejected   OpportunityStatus = "RISK_REJECTED"
	OpportunityBrokerRejected OpportunityStatus = "BROKER_REJECTED"
	OpportunityNoFill         OpportunityStatus = "NO_FILL"
	OpportunityPending        OpportunityStatus = "PENDING"
	OpportunityOpen           OpportunityStatus = "OPEN"
	OpportunityClosed         OpportunityStatus = "CLOSED"
)

// Opportunity explains what happened to every setup the detector flagged,
// approved or not -- the same funnel purpose the ICC-era Opportunity served,
// now built from scan lifecycle events (DETECTED/REJECTED/EXPIRED) plus the
// real orders and fills paper.Broker produced, instead of ICC
// Indication/Confirmation/SetupCandle transitions.
type Opportunity struct {
	ID             string             `json:"id"`
	Symbol         string             `json:"symbol"`
	Strategy       string             `json:"strategy"`
	Side           string             `json:"side"`
	StartedAt      time.Time          `json:"started_at"`
	ResolvedAt     time.Time          `json:"resolved_at"`
	Status         OpportunityStatus  `json:"status"`
	Reason         string             `json:"reason,omitempty"`
	DecisionCode   risk.RejectionCode `json:"decision_code,omitempty"`
	IntentID       string             `json:"intent_id,omitempty"`
	BrokerOrderID  string             `json:"broker_order_id,omitempty"`
	EntryQuantity  int64              `json:"entry_quantity"`
	ClosedQuantity int64              `json:"closed_quantity"`
}

type opportunityBuilder struct {
	opportunity Opportunity
	explicit    bool
}

func finalizeOpportunities(builders []*opportunityBuilder, orders []domain.BrokerOrder, fills []domain.Fill, resolvedAt time.Time) []Opportunity {
	ordersByIntent := make(map[string]domain.BrokerOrder, len(orders))
	entryByIntent := make(map[string]int64)
	closedByIntent := make(map[string]int64)
	for _, order := range orders {
		ordersByIntent[order.IntentID] = order
	}
	for _, fill := range fills {
		if fill.Role == domain.FillEntry {
			entryByIntent[fill.IntentID] += fill.Quantity
		} else {
			closedByIntent[fill.IntentID] += fill.Quantity
		}
	}
	result := make([]Opportunity, 0, len(builders))
	for _, builder := range builders {
		opportunity := builder.opportunity
		if opportunity.Status == "" {
			opportunity.Status = OpportunityUnconfirmed
			if opportunity.IntentID != "" {
				opportunity.EntryQuantity = entryByIntent[opportunity.IntentID]
				opportunity.ClosedQuantity = closedByIntent[opportunity.IntentID]
				order, exists := ordersByIntent[opportunity.IntentID]
				if exists {
					opportunity.BrokerOrderID = order.BrokerOrderID
				}
				switch {
				case !exists:
					opportunity.Status = OpportunityNoFill
				case order.Status == domain.OrderRejected:
					opportunity.Status = OpportunityBrokerRejected
				case opportunity.EntryQuantity > 0 && opportunity.ClosedQuantity >= opportunity.EntryQuantity:
					opportunity.Status = OpportunityClosed
				case opportunity.EntryQuantity > 0:
					opportunity.Status = OpportunityOpen
				case order.Status == domain.OrderAcknowledged || order.Status == domain.OrderPartiallyFilled:
					opportunity.Status = OpportunityPending
				default:
					opportunity.Status = OpportunityNoFill
				}
			}
		}
		if opportunity.ResolvedAt.IsZero() && opportunity.Status != OpportunityPending && opportunity.Status != OpportunityOpen {
			opportunity.ResolvedAt = resolvedAt
		}
		result = append(result, opportunity)
	}
	return result
}
