# Simple Local Lifecycle Implementation Plan

**Goal:** Provide `mise run up`, `down`, and `destroy` for the full discovery/scaling demo.

**Approved design:** `up` creates or resumes the dedicated kind cluster and prints ready localhost API/UI endpoints. Workers use Kyverno discovery and KEDA scale-to-zero. `down` stops managed tunnels and the verified node container, retaining all history. `destroy` removes only the owned cluster and lifecycle state. Docker must already be running. Shutdown interrupts active work; elapsed Temporal deadlines still matter.

**Architecture:** Reuse the infrastructure bootstrap with an explicit full mode; preserve existing experiment entrypoints. A lifecycle wrapper owns a container-ID record and managed tunnel records in `.local/lifecycle`. Subsequent up resumes without redeploying/rebootstrapping existing workloads. Initial/incomplete bootstrap is retryable. Cross-check Docker labels and exact container identity before mutation; refuse adoption of preexisting clusters.

**Review focus:** foreign/replaced clusters; stopped cluster recovery; interrupted bootstrap; stale/reused tunnel PIDs and occupied ports; complete registry convergence and native replica ownership.

## Task 1: Full bootstrap
- [x] Add failing real-chart/full-mode tests covering discovery RBAC, zero bootstrap, and replica-free steady state.
- [x] Extend `scripts/k8s-up.sh` with full mode; install policies before apps, render combined values, remove replicas from last-applied after bootstrap, wait on scalers rather than zero worker rollouts.
- [x] Run chart tests.

## Task 2: Owned lifecycle
- [x] Add failing executable shell-boundary tests for stop/destroy/foreign ownership and resume.
- [x] Add `scripts/local.sh`: exclusive lock, owned create/resume, full bootstrap, readiness and registration hash checks, managed localhost forwards, stop and destroy.
- [x] Add mise tasks and user docs.
- [x] Run race suite and manifest validation; exercise actual up, curl completion, down/resume/history and destroy on the newly owned cluster.

## Task 3: Review and publish
- [x] Self-review diff (no subagent tool available), record verification and limitations.
- [ ] Commit, push feature branch and open GitHub PR.

## Execution notes
- Baseline `mise exec -- go test ./...` passed before implementation.
- Worktree: `../temporal-k8s-orchestration-demo-lifecycle`, branch `feat/simple-local-lifecycle`.
- No changes to the original checkout. No automatic adoption of legacy clusters: fail with actionable instructions instead.
- `mise run validate` passed, including race tests, vet, policy tests, shell syntax, chart/graph validation and Compose config.
- Actual macOS ARM64 / Docker kind run: fresh full startup; UI/API readiness; keyed diamond completed with four results; all four workers observed at 0/0 afterward; repeated up; repeated down; resume with byte-identical completed keyed response; destroy from stopped state; repeated destroy. Test cluster removed afterward.
- Regression found and fixed: nohup's transient command line was recorded before exec. Deterministic delayed-exec test failed before fix and passed after recording the final ready tunnel identity.
- Review fix: interrupted bootstrap must strip replica ownership from previous apply annotations before retrying. Added failing full-retry test, then fixed it.
- Ruling: failed kind creation does not establish ownership, even if a container exists afterward, to avoid adopting a concurrent checkout's node. Application-bootstrap failures remain retryable; partial kind creation may require explicit operator cleanup.
- Ruling: repeated up resumes installed workloads without rebuilding, avoiding schema/application changes during active executions. Documented that source changes require deliberate recreation for this simplified path.
- Final review: self-review (no subagent tool). Linux execution and CI outcomes are not inferred from the local ARM64 run.
