package ctrader

import "tradex/internal/config"

type ReadinessInput struct {
	Config         Config
	Approval       *PromotionApproval
	Reconciliation Reconciliation
	Evidence       OperationalEvidence
	Safety         *SafetyGate
}

type ReadinessReport struct {
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons,omitempty"`
}

type ReadinessDocument struct {
	Config           Config              `json:"config"`
	Approval         *PromotionApproval  `json:"approval,omitempty"`
	Reconciliation   Reconciliation      `json:"reconciliation"`
	Evidence         OperationalEvidence `json:"evidence"`
	CircuitState     CircuitState        `json:"circuit_state"`
	KillSwitchActive bool                `json:"kill_switch_active"`
}

func CheckReadinessDocument(document ReadinessDocument) ReadinessReport {
	report := ReadinessReport{Ready: true}
	if err := document.Config.Validate(); err != nil {
		report.add("invalid adapter configuration: " + err.Error())
	}
	if document.Config.Mode == config.BrokerActiveLive {
		if document.Approval == nil {
			report.add("live promotion approval is missing")
		} else if err := document.Approval.Validate(document.Config); err != nil {
			report.add("invalid live promotion approval: " + err.Error())
		}
	}
	if !document.Reconciliation.Match {
		report.add("reconciliation is not matched")
	}
	if !document.Evidence.Healthy {
		report.add("operational evidence is unhealthy")
	}
	if document.CircuitState != CircuitClosed {
		report.add("circuit breaker is not closed")
	}
	if document.KillSwitchActive {
		report.add("kill switch is engaged")
	}
	return report
}

func CheckDeploymentReadiness(input ReadinessInput) ReadinessReport {
	report := ReadinessReport{Ready: true}
	if err := input.Config.Validate(); err != nil {
		report.add("invalid adapter configuration: " + err.Error())
	}
	if input.Config.Mode == config.BrokerActiveLive {
		if input.Approval == nil {
			report.add("live promotion approval is missing")
		} else if err := input.Approval.Validate(input.Config); err != nil {
			report.add("invalid live promotion approval: " + err.Error())
		}
	}
	if !input.Reconciliation.Match {
		report.add("reconciliation is not matched")
	}
	if !input.Evidence.Healthy {
		report.add("operational evidence is unhealthy")
	}
	if input.Safety == nil {
		report.add("safety gate is missing")
	} else {
		if input.Safety.CircuitState() != CircuitClosed {
			report.add("circuit breaker is not closed")
		}
		if input.Safety.KillSwitchActive() {
			report.add("kill switch is engaged")
		}
	}
	return report
}

func (r *ReadinessReport) add(reason string) {
	r.Ready = false
	r.Reasons = append(r.Reasons, reason)
}
