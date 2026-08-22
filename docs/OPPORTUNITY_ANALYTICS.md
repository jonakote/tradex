# Opportunity-level analytics

Completed trades are only one part of a strategy's funnel. Tradex now records each valid ICC indication as an opportunity and carries it through strategy, risk, and broker execution outcomes.

An opportunity can be:

- `UNCONFIRMED`: the setup never reached a continuation confirmation;
- `INVALIDATED`: the setup structure failed or correction expired;
- `RISK_REJECTED`: confirmation occurred but the account risk boundary vetoed it;
- `BROKER_REJECTED`: risk approved it but the broker rejected submission, such as a closed market or latency beyond intent expiry;
- `NO_FILL`: an approved order ended without an entry fill;
- `PENDING`: an order remains acknowledged or partially filled without an entry;
- `OPEN`: at least one entry fill exists but no complete exit has occurred;
- `CLOSED`: entry quantity has been fully matched by closing fills.

The opportunity record includes symbol, direction, lifecycle timestamps, risk rejection code when applicable, intent/order identity, entry quantity, and closed quantity. It is preserved in single and shared replay results and is summarized separately in research reports. Opportunity counts must not be substituted for completed-trade win rate: an invalidated or missed setup has no realized P&L.

Use the funnel to detect survivorship and execution bias. For example, a high completed-trade win rate alongside many risk rejections, no-fills, or unconfirmed setups is not equivalent to a high executable opportunity win rate. Review the raw opportunity records, rejection reasons, spread/liquidity assumptions, and untouched holdout periods together.
