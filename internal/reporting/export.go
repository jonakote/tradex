package reporting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

func WriteBundleNew(jsonPath, markdownPath string, report Report) error {
	if jsonPath == "" || markdownPath == "" || jsonPath == markdownPath {
		return errors.New("distinct JSON and Markdown report paths are required")
	}
	if err := report.Verify(); err != nil {
		return err
	}
	for _, path := range []string{jsonPath, markdownPath} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("report output already exists: %s", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect report output %s: %w", path, err)
		}
	}
	jsonOutput, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	jsonOutput = append(jsonOutput, '\n')
	markdownOutput := []byte(RenderMarkdown(report))
	if err := writeNew(jsonPath, jsonOutput); err != nil {
		return err
	}
	if err := writeNew(markdownPath, markdownOutput); err != nil {
		_ = os.Remove(jsonPath)
		return err
	}
	return nil
}

func RenderMarkdown(report Report) string {
	var output bytes.Buffer
	fmt.Fprintln(&output, "# Tradex Research Report")
	fmt.Fprintln(&output)
	fmt.Fprintf(&output, "- Schema: `%s`\n", inlineCode(report.SchemaVersion))
	fmt.Fprintf(&output, "- Experiment: `%s`\n", inlineCode(report.ExperimentID))
	fmt.Fprintf(&output, "- Mode: `%s`\n", inlineCode(report.Mode))
	fmt.Fprintf(&output, "- Manifest SHA-256: `%s`\n", inlineCode(report.ManifestSHA256))
	fmt.Fprintf(&output, "- Source-result SHA-256: `%s`\n", inlineCode(report.SourceResultSHA256))
	fmt.Fprintf(&output, "- Report SHA-256: `%s`\n", inlineCode(report.ReportSHA256))

	fmt.Fprintln(&output, "\n## Account performance")
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "| Metric | Value |")
	fmt.Fprintln(&output, "|---|---:|")
	writePerformanceRows(&output, report.Account)

	fmt.Fprintln(&output, "\n## Execution funnel")
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "| Metric | Count |")
	fmt.Fprintln(&output, "|---|---:|")
	fmt.Fprintf(&output, "| Decisions | %d |\n", report.Decisions.Total)
	fmt.Fprintf(&output, "| Approved | %d |\n", report.Decisions.Approved)
	fmt.Fprintf(&output, "| Rejected | %d |\n", report.Decisions.Rejected)
	fmt.Fprintf(&output, "| Orders | %d |\n", report.Execution.Orders)
	fmt.Fprintf(&output, "| Rejected orders | %d |\n", report.Execution.RejectedOrders)
	fmt.Fprintf(&output, "| Cancelled orders | %d |\n", report.Execution.CancelledOrders)
	fmt.Fprintf(&output, "| Open partially filled orders | %d |\n", report.Execution.OpenPartiallyFilledOrders)
	fmt.Fprintf(&output, "| Partially executed orders | %d |\n", report.Execution.PartiallyExecutedOrders)
	fmt.Fprintf(&output, "| Entry fills | %d |\n", report.Execution.EntryFills)
	fmt.Fprintf(&output, "| Closing fills | %d |\n", report.Execution.ClosingFills)

	fmt.Fprintln(&output, "\n## Opportunity outcomes")
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "| Outcome | Count |")
	fmt.Fprintln(&output, "|---|---:|")
	fmt.Fprintf(&output, "| Total opportunities | %d |\n", report.Opportunities.Total)
	fmt.Fprintf(&output, "| Unconfirmed | %d |\n", report.Opportunities.Unconfirmed)
	fmt.Fprintf(&output, "| Invalidated | %d |\n", report.Opportunities.Invalidated)
	fmt.Fprintf(&output, "| Risk rejected | %d |\n", report.Opportunities.RiskRejected)
	fmt.Fprintf(&output, "| Broker rejected | %d |\n", report.Opportunities.BrokerRejected)
	fmt.Fprintf(&output, "| No fill | %d |\n", report.Opportunities.NoFill)
	fmt.Fprintf(&output, "| Pending | %d |\n", report.Opportunities.Pending)
	fmt.Fprintf(&output, "| Open | %d |\n", report.Opportunities.Open)
	fmt.Fprintf(&output, "| Closed | %d |\n", report.Opportunities.Closed)

	if len(report.Decisions.RejectedByCode) > 0 {
		fmt.Fprintln(&output, "\n### Rejections")
		fmt.Fprintln(&output)
		fmt.Fprintln(&output, "| Code | Count |")
		fmt.Fprintln(&output, "|---|---:|")
		for _, code := range sortedKeys(report.Decisions.RejectedByCode) {
			fmt.Fprintf(&output, "| %s | %d |\n", tableCell(code), report.Decisions.RejectedByCode[code])
		}
	}

	if len(report.Symbols) > 0 {
		fmt.Fprintln(&output, "\n## Per-symbol performance")
		fmt.Fprintln(&output)
		fmt.Fprintln(&output, "| Symbol | Trades | Wins | Losses | Win rate | Net P&L (minor) | Return |")
		fmt.Fprintln(&output, "|---|---:|---:|---:|---:|---:|---:|")
		for _, symbol := range sortedKeys(report.Symbols) {
			value := report.Symbols[symbol]
			fmt.Fprintf(&output, "| %s | %d | %d | %d | %.2f%% | %d | %.2f%% |\n",
				tableCell(symbol), value.Trades, value.Wins, value.Losses, value.WinRate*100,
				value.NetProfitMinor, float64(value.ReturnBPS)/100)
		}
	}

	fmt.Fprintln(&output, "\n## Final portfolio")
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "| Metric | Value |")
	fmt.Fprintln(&output, "|---|---:|")
	fmt.Fprintf(&output, "| Initial equity (minor) | %d |\n", report.Portfolio.InitialEquityMinor)
	fmt.Fprintf(&output, "| Final equity (minor) | %d |\n", report.Portfolio.FinalEquityMinor)
	fmt.Fprintf(&output, "| Open positions | %d |\n", report.Portfolio.OpenPositions)
	fmt.Fprintf(&output, "| Aggregate open risk (minor) | %d |\n", report.Portfolio.AggregateOpenRiskMinor)
	fmt.Fprintf(&output, "| Current drawdown | %.2f%% |\n", float64(report.Portfolio.DrawdownBPS)/100)
	if len(report.Portfolio.BucketOpenRiskMinor) > 0 {
		fmt.Fprintln(&output, "\n### Open-risk buckets")
		fmt.Fprintln(&output)
		fmt.Fprintln(&output, "| Bucket | Gross risk (minor) | Directional risk (minor) |")
		fmt.Fprintln(&output, "|---|---:|---:|")
		for _, bucket := range sortedKeys(report.Portfolio.BucketOpenRiskMinor) {
			fmt.Fprintf(&output, "| %s | %d | %d |\n", tableCell(bucket),
				report.Portfolio.BucketOpenRiskMinor[bucket], report.Portfolio.DirectionalRiskMinor[bucket])
		}
	}

	fmt.Fprintln(&output, "\n## Interpretation guardrails")
	fmt.Fprintln(&output)
	for _, caveat := range report.Caveats {
		fmt.Fprintf(&output, "- %s\n", strings.TrimSpace(caveat))
	}
	return output.String()
}

func writePerformanceRows(output *bytes.Buffer, value Performance) {
	fmt.Fprintf(output, "| Completed trades | %d |\n", value.Trades)
	fmt.Fprintf(output, "| Wins / losses / breakeven | %d / %d / %d |\n", value.Wins, value.Losses, value.Breakeven)
	fmt.Fprintf(output, "| Win rate | %.2f%% |\n", value.WinRate*100)
	fmt.Fprintf(output, "| Win-rate 95%% Wilson interval | %.2f%%–%.2f%% |\n", value.WinRateLower95*100, value.WinRateUpper95*100)
	fmt.Fprintf(output, "| Net P&L (minor) | %d |\n", value.NetProfitMinor)
	fmt.Fprintf(output, "| Return | %.2f%% |\n", float64(value.ReturnBPS)/100)
	fmt.Fprintf(output, "| Expectancy (minor/trade) | %.2f |\n", value.ExpectancyMinor)
	if value.ProfitFactorInfinite {
		fmt.Fprintln(output, "| Profit factor | ∞ (no losing P&L) |")
	} else {
		fmt.Fprintf(output, "| Profit factor | %.4f |\n", value.ProfitFactor)
	}
	fmt.Fprintf(output, "| Commission (minor) | %d |\n", value.TotalCommissionMinor)
	fmt.Fprintf(output, "| Rollover/swap (minor) | %d |\n", value.TotalRolloverMinor)
	fmt.Fprintf(output, "| Max drawdown (minor) | %d |\n", value.MaxDrawdownMinor)
	fmt.Fprintf(output, "| Max consecutive losses | %d |\n", value.MaxConsecutiveLosses)
}

func inlineCode(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.ReplaceAll(value, "`", "\\`")
}

func tableCell(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.ReplaceAll(value, "|", "\\|")
}

func writeNew(path string, content []byte) (writeErr error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create report output %s: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); writeErr == nil && closeErr != nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = os.Remove(path)
		}
	}()
	written, err := file.Write(content)
	if err != nil {
		return fmt.Errorf("write report output %s: %w", path, err)
	}
	if written != len(content) {
		return fmt.Errorf("write report output %s: short write", path)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync report output %s: %w", path, err)
	}
	return nil
}
