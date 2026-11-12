# Minimal demo execution record

The approved plan is preserved as a planning baseline; this record tracks execution.
The user subsequently authorized implementation and seven stacked PRs. Work is in
a separate worktree; the original checkout and existing developer cluster were
not used as disposable fixtures. No merge to main is requested.

## Stack

- [x] Task 1 — shared validation and `/graph`: PR #15, base main.
- [x] Task 2 — deployment-derived complete-graph readiness: PR #16, base #15.
- [x] Task 3 — disposable public lifecycle integration: PR #17, base #16.
- [x] Task 4 — remove alternate runtimes/experiments: PR #18, base #17.
- [x] Task 5 — full-only discovery/scaling, remove static registry/CLI: PR #19, base #18.
- [x] Task 6 — test consolidation with explicit retained coverage: PR #20, base #19.
- [x] Task 7 implementation — final docs/CI, base #20. Exact-head disposable
  acceptance is tracked in the final PR's checks (pending at publication).

## Implementation adjustments

- Runner uses dedicated `demo_integration` tag, rather than the planned generic
  `integration` tag. CI exposed accidental selection by the intermediate legacy
  suite. A failing-first selector test justified the fix, propagated from #17
  through descendants with ordinary merges; no history was rewritten.
- Some full-chart test consolidation moved into Task 5 because deleted chart
  modes could no longer render. Task 6 finishes scaler assertion consolidation.
- Final review added a post-validation deadline check so a response delayed past
  120 seconds cannot succeed. Its simulated-clock regression failed before the
  fix. Additional 422/connection/extra-worker/malformed-state cases cover the
  planned polling matrix. A second failing-first check ensures curl failure codes
  survive timeout diagnostics, distinguishing connection failure from timeout
  without printing arbitrary response/error bodies.
- Fast tests use `-count=1` so Go's result cache cannot skip changed external
  shell/Helm/policy inputs. CI setup explicitly includes the pinned Kyverno CLI.
- No subagent or independent-review tool was available. Review is self-review;
  no independent reviewer approval is claimed. Context checkpoints are retained
  in ignored `.local/minimal-progress/`; no manual compaction tool was available.

## Verification

- Baseline Go suite passed before implementation.
- Each implementation stage ran focused tests and race-enabled validation; both
  app image builds passed after runtime/CLI deletion.
- Full replacement lifecycle passed on disposable Linux CI at step 4,
  [run 43100020101](https://github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/actions/runs/43100020101).
- Local integration deliberately refused the existing `temporal-demo` cluster.
  Its preexisting node was not stopped, adopted or deleted. No fresh ARM64 live
  acceptance of the final minimal demo is claimed.
- Final uncached `mise run validate`, `git diff --check` and active-documentation
  link checks passed locally. Final exact-head CI evidence belongs to the final
  PR checks, not inferred from the earlier passing run.

## Review focus

1. Startup compares full canonical nodes/dependencies derived from rendered
   Services, not merely a valid subset or copied API hash. Bookkeeping is never
   mounted into the runtime. Immediate/one-second polling has bounded requests.
2. Shared validation remains after durable lookup. Service/gateway race, restart,
   ambiguity and metadata tests remain; HTTP tests keep transport responsibilities.
   See [coverage ownership](../../testing.md) for actual test names.
3. Bootstrap-zero/replica-free handoff and interrupted-bootstrap annotation cleanup
   remain. Initialized up resumes installed images; documentation requires explicit
   destructive recreation to upgrade old images.
4. Runner refuses preexisting cluster/state before setup and destroys only a node
   recorded by owned lifecycle creation. Port/PID, foreign context, replaced-node,
   immutable credentials and PVC protections remain.
5. Removed runtimes lose only unsupported scenarios. Core graph, policy, schema,
   informer synchronization, workflow histories, lifecycle and idempotency checks
   have retained homes. Only two CI jobs remain; no duplicate fast-suite pass.

Out of scope by approved design: production ingress/authentication, automatic
history-preserving upgrades, alternate registries/runtimes, long-workflow scaler
experiments and generic failure-injection infrastructure. No additional demo mode
or large abstraction layer was added.
