# Pepperstone cTrader adapter boundary

Tradex now contains a cTrader adapter boundary for Pepperstone accounts. It is intentionally transport-injected: the repository does not claim to implement or validate the cTrader Open API protocol, authentication flow, symbol mapping, or production endpoint behavior.

The adapter requires:

- explicit paper/live environment and `SHADOW`, `ACTIVE_DEMO`, or `ACTIVE_LIVE` mode;
- endpoint, client identity, and a credential reference (secret material is not stored in scenario JSON);
- a frozen experiment manifest SHA-256;
- for live mode, an operator approval matching that manifest, with timestamp and reason;
- a successful remote snapshot reconciliation before submission or cancellation.

Reconciliation compares broker order IDs, status, remaining quantity, position IDs, and quantities. A mismatch or unavailable snapshot blocks order mutations. The risk engine's adapter-connected and reconciliation-mismatch vetoes remain independent controls.

The next connectivity step is a separately reviewed cTrader transport implementation with demo-only contract tests, authentication rotation, rate-limit handling, broker symbol mapping, server-time conversion, and durable remote-order correlation. Until that exists, the adapter boundary and fake transport tests are the only supported behavior; no live order path is enabled by default.

`DemoTransport` now provides a deterministic remote-state simulator for adapter contract tests and demo-forward harnesses. It supports acknowledged orders, cancellation, open-order snapshots, positions, and reconciliation. It is deliberately not a cTrader Open API client and cannot be used as evidence that production connectivity or broker behavior has been validated.

The protocol session contract models the official flow: OAuth 2.0 credentials, application authorization, account authorization, JSON or protobuf serialization, and heartbeat scheduling. cTrader's official documentation describes these requirements in [account authentication](https://help.ctrader.com/open-api/account-authentication/), [message transport](https://help.ctrader.com/open-api/sending-receiving-protobuf/), and [connection/heartbeat guidance](https://help.ctrader.com/open-api/connection/). The Go package does not yet serialize official protobuf messages or open a production socket.

The next protocol slice is also kept network-free: `TokenManager` validates and atomically refreshes OAuth tokens through an injected `TokenRefresher`; `EncodeTCPFrame`/`DecodeTCPFrame` implement the four-byte length-prefixed TCP envelope with bounded fragmented reads; and `EncodeWebSocketMessage` returns a detached payload for a WebSocket client library to frame. None of these utilities open connections or enable live orders.

`MessageEnvelope` and `RequestTracker` now provide the next boundary: generated protobuf payloads can be correlated using deterministic client message IDs, and pending requests can be completed or reset on disconnect. `ProtocolError` preserves broker error code and retriability policy without hard-coding an incomplete enum.

`ProtocolClient` composes those contracts with the session state machine. It accepts an injected `MessageWriter`, blocks mutations before account authorization, cleans up failed writes, correlates responses, and resets pending requests on disconnect. A real writer remains a separate demo-only implementation task.

`TCPMessageWriter` and `WebSocketMessageWriter` now implement that seam over injected sinks. TCP receives the length-prefixed frame; WebSocket receives a detached payload and owns its protocol framing. The writers never establish connections or choose TLS/authentication settings.

`DemoProtocolHarness` exercises the complete in-memory request path: session gate → client message ID → injected response → correlation. It is a contract-test harness only and must not be treated as evidence of broker connectivity or fill behavior.

Typed `OperationRequest` validation now covers authorization, order submission, cancellation, and snapshot intents. It enforces account identity and requires `domain.ApprovedOrder` for submissions; serialized payload construction remains transport-specific and no live endpoint is enabled.

`OperationResponse` provides the matching decoded-result boundary. It validates correlation, account, payload type, broker IDs, and snapshot timestamps before `ProtocolClient` completes a request. A real protobuf decoder can populate this model without changing adapter or risk code.

The deterministic `DemoTransport` now bridges typed submit, cancel, and snapshot operations to its in-memory order state. This validates the operation/response contracts against reconciliation-visible state, but it is still not evidence of cTrader connectivity, execution quality, or live broker behavior.

Retry policy is conservative: `ClassifyRetry` retries only explicitly retriable protocol errors after reconnect. Unknown errors and broker rejections are not automatically retried. Demo submission remains idempotent by strategy intent ID, preventing duplicate orders during safe replay.

`ProtocolClient` now wraps the session heartbeat contract: callers can record heartbeats, detect deadlines, reset pending requests on timeout, and reconnect explicitly. Network dial, TLS, and broker heartbeat messages remain outside this package.

Operational safety is now explicit: `CircuitBreaker` trips after configurable failures and probes after cooldown; `KillSwitch` blocks requests until an operator clears it; `SafetyGate` composes both and can be attached to `ProtocolClient`. These controls are independent of strategy signals and do not authorize live trading.

`AuditEvent`/`AuditRecorder` provide metadata-only operational evidence for safety blocks, protocol failures, heartbeat timeouts, disconnects, and reconnects. OAuth material and serialized request payloads are intentionally excluded.

`JournalAuditRecorder` persists these events through the existing tamper-evident journal. It uses client message IDs or account IDs as correlations and exposes append errors; the recorder still stores no credentials or raw protocol payloads.

`BuildOperationalEvidence` converts the journal stream into an operator-facing summary of safety and connection events. An empty stream is treated as missing evidence; recorder errors make the report unhealthy and are surfaced explicitly.

The CLI command `ctrader-evidence <audit.jsonl> <evidence.json>` verifies the journal, writes the summary, and returns a non-zero status for missing or invalid evidence. It is read-only with respect to the journal and never enables broker connectivity.

`CheckDeploymentReadiness` combines those evidence checks with configuration, reconciliation, safety-gate, and live-approval controls. It returns explicit reasons and is fail-closed; a ready report is not itself permission to connect a live broker.

The CLI command `ctrader-readiness <readiness.json> <report.json>` evaluates a serialized readiness snapshot for deployment automation. It is strict about unknown JSON fields, refuses overwrite, and returns failure when the report is not ready.

`ReadinessArtifact` is the deployment handoff form: it binds manifest and evidence hashes, reconciliation time, approval metadata, and a canonical HMAC signature. Verification is required before treating the artifact as evidence; signing keys must be supplied externally and are never serialized.

The CLI commands `ctrader-artifact-create` and `ctrader-artifact-verify` provide the handoff workflow. Key files are read only at execution time, outputs are protected from overwrite, and successful creation does not connect to any broker.

Artifacts include signer metadata and can be checked with an allow-list policy using `VerifyWithPolicy`. This supports controlled key rotation and revocation; signing keys remain external to the artifact.

The CLI supports explicit key metadata during artifact creation and a policy file during verification. This allows operators to rotate keys without accepting artifacts from revoked identities or signer versions.

Freshness policy can additionally reject expired artifacts, stale reconciliations, or timestamps from the future. Limits are supplied in seconds through the CLI policy document and evaluated at verification time.

Artifacts also carry a signed scope containing broker, account, environment, and manifest identity. This prevents a valid handoff from being reused for a different account or deployment target.

Audience and purpose are also signed and policy-checkable. Deployment verification can require the expected workflow identity, preventing cross-workflow artifact reuse.

Artifacts declare a signed schema version, and verification policy can allow-list supported versions. This makes future schema migrations explicit and prevents silent interpretation of incompatible artifacts.

Artifact hashes can be revoked with reasons and timestamps through `RevocationList`; verification rejects hashes listed in policy. This supports superseding or withdrawing a handoff without altering the original journal record.

Revocations can now be appended as `CTRADER_ARTIFACT_REVOKED` journal events and replayed with `LoadRevocations`, making revocation state durable and reviewable.

The CLI can export this state with `ctrader-revocations` or verify an artifact against it with `ctrader-artifact-verify-journal`. The journal remains read-only during verification.

Revocation replay applies explicit precedence rules: identical records are safe to replay, older records cannot overwrite newer state, same-time reason conflicts fail closed, and newer records supersede prior reasons.

The first broker-specific connector seam is now `PepperstoneDemoConfig`. It is demo-only, requires external OAuth/token references, and intentionally stops before network dialing or protobuf/JSON operation encoding.

Local credential configuration uses an ignored `.env` file. See the repository `.env.example` for variable names. Client secrets, authorization codes, access tokens, and refresh tokens must never be committed or pasted into source files. The next connector step is the OAuth callback and token-exchange flow; no order request will be enabled by that step.

The OAuth client now supports the official authorization-code and refresh-token exchanges through `OAuthClient`. It is environment-backed and HTTP-injectable for tests; actual account authentication still requires a locally run callback and user consent.

Progress is currently paused pending activation of the registered cTrader Open API application. Once active, resume with the OAuth URL and exchange commands, then proceed to application/account authorization and demo snapshot reconciliation. No broker request or live order path is enabled while approval is pending.

CLI usage is now available: generate a consent URL with `ctrader-oauth-url`, then exchange the returned callback code with `ctrader-oauth-exchange`. Token files are local, mode `0600`, and protected from overwrite.
