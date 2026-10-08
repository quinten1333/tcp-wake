# Requirements Traceability Matrix — tcp-wake

> Baseline: `docs/specs/SRS.md` v0.4 and `docs/architecture.md`.
> One row per requirement. `req_id`, `requirement`, `source`, `priority`,
> `verify_method`, `test_id`, `status`. The SRS assigns no separate priority, so
> the SRS goal label (G1–G4) is carried in the `priority` column; it is the
> goal the requirement exists to serve. `verify_method` is `test`, or
> `inspection + test` where the SRS check is an inspection with an executable
> proxy check alongside it. The inspection halves are the README checklists
> (NFR-4, NFR-7, NFR-8) and `scripts/check-deploy.sh`.
>
> The Go test names are the `test_id`s; `go test ./...` runs them all.

| req_id | requirement | source | priority | verify_method | test_id | status |
|---|---|---|---|---|---|---|
| FR-1 | When the system receives a request bound for hypha while hypha is healthy, the system shall forward the request to hypha and return hypha's response to the client. | SRS §3 | G2 | test | TestFR1HealthyRequestForwarded | verified |
| FR-2 | When the system receives a request bound for hypha while hypha is not healthy, the system shall hold the request open without sending a response. | SRS §3 | G1 | test | TestFR2HoldsUntilHealthyAndSendsNoBytes | verified |
| FR-3 | When the system receives a request bound for hypha while hypha is not healthy, the system shall execute the wake command once for that request. | SRS §3 | G1 | test | TestFR3FiveRequestsProduceFiveExecutions | verified |
| FR-4 | When hypha becomes healthy, the system shall forward every held request to hypha. | SRS §3 | G1 | test | TestFR4AndFR5AllHeldRequestsForwardInParallel | verified |
| FR-5 | When hypha becomes healthy, the system shall begin forwarding each held request without waiting for any other held request to finish. | SRS §3 | G1 | test | TestFR4AndFR5AllHeldRequestsForwardInParallel | verified |
| FR-6 | While the system holds a request, the system shall send no response bytes to the client for that request. | SRS §3 | G1 | test | TestFR6NoBytesSentWhileHeld | verified |
| FR-7 | When a client closes the connection for a held request, the system shall discard that request. | SRS §3 | G1 | test | TestFR7ClientCloseDiscardsHeldRequest | verified |
| FR-8 | If hypha does not become healthy within the configured wait bound measured from the request's arrival, then the system shall return an error response to the client naming the wait bound. | SRS §3 | G1 | test | TestFR8WaitBoundExpiryGets504NamingBound | verified |
| FR-9 | When the system receives a request bound for a service other than hypha, the system shall not execute the wake command. | SRS §3 | G4 | test | TestFR9NoWakeForTrafficNotBoundForHypha | verified |
| FR-10 | When hypha's health endpoint returns a ready status within the probe timeout, the system shall treat hypha as healthy. | SRS §3 | G1 | test | TestFR10ProbeReadySetsHealthy | verified |
| FR-11 | When hypha's health endpoint does not return a ready status within the probe timeout, the system shall treat hypha as not healthy. | SRS §3 | G1 | test | TestFR11Probe503StaysNotHealthy; TestFR11ProbeTimeoutStaysNotHealthy | verified |
| FR-12 | While no request bound for hypha is pending, the system shall not execute the wake command. | SRS §3 | G4 | test | TestFR12NoExecWhileIdle; TestFR12NoProbeWhileIdle; TestFR12NoLogLinesWhileIdle | verified |
| FR-13 | When the system forwards a request to hypha, the system shall transmit the request's method, path, headers, and body unchanged. | SRS §3 | G2 | test | TestFR13IntakeRetainsBytesVerbatim; TestFR13ForwardedRequestBytesUnchanged | verified |
| FR-14 | When the system receives a response from hypha, the system shall transmit the response's status, headers, and body to the client unchanged. | SRS §3 | G2 | test | TestFR14ResponseRelayedUnchanged; TestIF2ForwardAndRelayRequestResponse | verified |
| FR-15 | When hypha's response is streamed, the system shall relay each chunk to the client as the chunk arrives. | SRS §3 | G2 | test | TestFR15StreamsChunksUnbuffered | verified |
| FR-16 | When the system restarts while requests are held, the system shall not forward those requests to hypha. | SRS §3 | G1 | test | TestFR16RestartMidHoldForwardsNothing; TestFR16ShutdownDuringWakeWritesNoResponse | verified |
| FR-17 | If the wake command exits with a non-zero status, then the system shall return an error response to that request's client immediately, naming the wake command as the failed component. | SRS §3 | G1 | test | TestFR17ClientGets500NamingWakeCommand; TestFR17NonZeroExitIsReported | verified |
| FR-18 | If a forward to hypha fails at the transport level after the connection is established, then the system shall return an error response to that request's client. | SRS §3 | G1 | test | TestFR18TransportFailureGives502 | verified |
| FR-19 | If a forward to hypha fails at the transport level, then the system shall probe hypha's health endpoint and set hypha's state from that probe's result. | SRS §3 | G1 | test | TestFR19TransportFailureTriggersProbe | verified |
| FR-20 | If a request body exceeds the configured held body cap, then the system shall return an error response naming the cap. | SRS §3 | G1 | test | TestFR20BodyOverCapGets413 | verified |
| NFR-1 | The system shall return the first response for a request received while hypha is not healthy within 120 s of receiving that request on the reference network. | SRS §4 | G1 | test | TestNFR1FirstResponseWithinBound; TestNFR1DefaultWaitBoundCoversBoot | verified |
| NFR-2 | The system shall return the first response for a request received while hypha is healthy within 2 s of receiving that request on the reference network. | SRS §4 | G2 | test | TestNFR2HealthyFirstResponseUnder2s | verified |
| NFR-3 | The system shall add no more than 50 ms of latency to each streamed chunk it relays on the reference network. | SRS §4 | G2 | test | TestNFR3StreamedChunkLatencyUnder50ms | verified |
| NFR-4 | While the system holds a request, the system shall apply no read or write deadline to that request's connection. | SRS §4 | G1 | inspection + test | TestNFR4ListenerSetsNoDeadline; TestNFR4NoDeadlineCallsInSource | verified |
| NFR-5 | The system shall hold at least 8 concurrent requests bound for hypha without discarding any. | SRS §4 | G1 | test | TestNFR5EightConcurrentAllGetResponse | verified |
| NFR-6 | The system shall return exactly one response for every accepted request whose client connection stays open. | SRS §4 | G1 | test | TestNFR6ExactlyOneResponsePerAcceptedRequest; TestNFR6OnlyErrorsDotGoWritesAResponse | verified |
| NFR-7 | The system shall execute the wake command with the privileges that command requires. | SRS §4 | G3 | inspection + test | TestNFR7TriggerExecsEtherwake | verified |
| NFR-8 | The system shall run every part of the system other than the wake command without elevated privileges. | SRS §4 | G3 | inspection + test | TestNFR8NoPrivilegeEscalationInSource | verified |
| IF-1 | The system shall accept a request from a client over HTTP and return hypha's response over the same connection. | SRS §5 | G2 | test | TestIF1ListenerHandsOffRequest | verified |
| IF-2 | The system shall forward a held request to hypha over HTTP and relay hypha's response to the client. | SRS §5 | G1 | test | TestIF2ForwardAndRelayRequestResponse | verified |
| IF-3 | The system shall treat hypha as healthy only when hypha's health endpoint returns HTTP 200 with body {"status":"ok"} within the probe timeout. | SRS §5 | G1 | test | TestIF3ReadyRequiresStatusAndBody; TestIF3ProbeHitsConfiguredPath | verified |
| IF-4 | The system shall execute the wake command as a child process and read its exit status. | SRS §5 | G1 | test | TestIF4WakeExecutesChildAndReadsExitStatus | verified |
| IF-5 | The system shall read the wait bound, hypha's MAC address, the wake interface, hypha's address, the health endpoint path, the probe cadence, the probe timeout, and the held body cap from a configuration file. | SRS §5 | G1 | test | TestIF5EveryKeyComesFromTheFile | verified |
| IF-6 | The system shall write one log line per wake-command execution. | SRS §5 | G1 | test | TestIF6OneLogLinePerExecution | verified |
