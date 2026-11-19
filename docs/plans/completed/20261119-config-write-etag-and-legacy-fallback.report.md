# Report: 2026 11 19 config write etag and legacy fallback
plan: /Users/echeg/Projects/CPA/CLIProxyAPI-migration/.loopai/worktrees/config-write-etag-and-legacy-fallback/docs/plans/20261119-config-write-etag-and-legacy-fallback.md | branch: config-write-etag-and-legacy-fallback | base: main | mode: full | executor: claude | task model: claude:opus:high | review model: claude:opus:xhigh | started: 2026-11-19T16:43:27Z | finished: 2026-11-19T17:45:25Z

## Summary
- task iterations: 4
- failed retries: 0
- external reviewers: 1
- post-review iterations: 1
| phase | duration_ms |
|---|---:|
| evaluation | 1012370 |
| external review | 675739 |
| internal review | 1512501 |
| other | 0 |
| tasks | 517608 |

## Change scope
- commits: 8
- files: 10
- additions: 698
- deletions: 76
| status | path |
|---|---|
| M | config.example.yaml |
| A | docs/backlog/removed-claude-fallback-node-revives-legacy-block.md |
| M | docs/management-api-v8.md |
| M | docs/plans/20261119-config-write-etag-and-legacy-fallback.md |
| M | internal/api/handlers/management/config_basic.go |
| A | internal/api/handlers/management/config_basic_write_test.go |
| M | internal/api/handlers/management/config_v8.go |
| M | internal/api/handlers/management/config_v8_etag_test.go |
| M | internal/api/handlers/management/routing_providers_write.go |
| M | internal/api/handlers/management/routing_providers_write_test.go |

## Evidence
_assessment unavailable_

## Risk
_assessment unavailable_

## Merge danger
_assessment unavailable_

## Migrations and operational steps
_assessment unavailable_

## Plan deviation
_assessment unavailable_
### Added
- none
### Blocked
- ⚠️ `ConfigV8` reads the file before writing, so a directory path fails with `read_failed`; the handler test uses a read-only file instead (`TestConfigV8WriteFailureOmitsETag`), and the directory case is covered by `TestWriteConfigBytesWriteFailure`
### Skipped
- none

## Backlog
| status | path | title |
|---|---|---|
| A | docs/backlog/removed-claude-fallback-node-revives-legacy-block.md | A write that removes providers.claude.fallback revives an ignored legacy claude-fallback |

## External review
### codex:gpt-6.2-astra:xhigh
- label: codex
- iterations: 5
- duration_ms: 1688071
- ended by: done
- had findings: true
#### Iteration 1
- truncated: false
Reviewer output:
```
- **P2 — A PATCH can leave Claude-to-Codex fallback enabled after the client clears it.** `internal/api/handlers/management/routing_providers_write.go:62` checks whether the submitted fallback is empty, not whether the merged fallback is empty. With `target: codex` and valid mappings stored, a PATCH sending `providers.claude.fallback: {"target":null}` alongside `claude-fallback: {}` should retain the mappings and default the target to `off`. Instead, conversion restores the previous `codex` target and returns success. When the PATCH names the modern fallback, check the merged node before converting.
- **P2 — A misspelled fallback field can pass strict validation.** `internal/api/handlers/management/routing_providers_write.go:125` uses `yaml.Node.Decode`, which ignores unknown fields. When a write sends `providers.claude.fallback: {"targte":"off"}` alongside a valid enabled legacy block, the typo makes the modern node appear empty. Conversion replaces it before `ValidateV8Config` sees it, so the request succeeds with fallback enabled instead of rejecting the typo. Preserve unrecognized nodes for strict validation.
- **P3 — The ETag test panics if the saved config cannot load.** `internal/api/handlers/management/config_v8_etag_test.go:206` dereferences `loaded` in the failure message even when `LoadConfig` returns `nil, err`. Check `err` separately so the test reports the load error.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the external review findings rigorously.Checking whether strict validation rejects unknown fields in the fallback node, then I'll fix findings 1-3.Findings 1 and 2 both trace to the conversion condition this branch changed in 6ec01f71, so they're in scope. Applying the fixes now.Now updating the function comment to describe the merged-node rule:Now finding 3, the nil dereference in the ETag test:All pass. Confirming the new tests actually catch the old behavior by temporarily restoring the previous logic:All three new tests fail on the old logic and pass with the fix. Now updating the doc's legacy-write rules, which describe the old condition:Running the full verification (gofmt, vet, build, the whole test suite):I fixed all three findings. They're left uncommitted for the next review pass. gofmt is clean, go vet and the build pass, go test ./... passes, and go test -race passes for the management package.

- P2, PATCH restores a cleared target (valid, fixed): Commit 6ec01f71 on this branch changed the conversion condition in routing_providers_write.go to look at the written providers.claude.fallback node. Before that commit, any write that named the node skipped the conversion. So this bug is new on the branch. I reproduced it: stored target codex with mappings, then a PATCH of {"target":null} plus claude-fallback: {}. The empty legacy patch was converted and target codex came back. The fix is a new function, convertsLegacyClaudeFallback. A write that doesn't send the modern node is still always converted, as older panels need. A write that does send it is converted only when the merged node is empty, which is when load would actually read the legacy block. In that scenario the mappings are now kept and the target loads as off. This works the same for a PUT, because there the merged node is the written node. The existing "PATCH of both blocks" case, an empty node over a stored empty node, still converts.
- P2, misspelled field slips past strict validation (valid, fixed): yaml.Node.Decode ignores unknown keys, so {"targte":"off"} looked empty. The conversion then replaced it before ValidateV8Config ran. unsetClaudeFallback now re-encodes the node and decodes it with KnownFields(true), the same strictness ValidateV8Config uses. A node with an unknown field now counts as set, so it is not converted and the validator rejects it with 400 invalid_config.
- P3, nil dereference in config_v8_etag_test.go (valid, fixed): The test now checks the LoadConfig error first with t.Fatal(err). The port and keys check comes after.

I added three subtests to TestConfigV8LegacyClaudeFallbackNextToZeroNode: the cleared-target PATCH, and a PATCH and a PUT of routing each carrying the typo. The typo cases expect 400 with the field named and the file unchanged. With the old logic temporarily put back, all three new subtests fail; with the fix they pass.

I also updated docs/management-api-v8.md to describe the new rules: a PATCH is judged by the merged node, and a fallback node with an unknown field is rejected. I filed nothing to the backlog, because all three findings are in code this branch changed.

The pattern behind both P2s: for a PATCH, decide by the merged result, as load will read it, not by the request body. A body can look empty while the merged result isn't.
```
#### Iteration 2
- truncated: false
Reviewer output:
```
The three previously reported cases are fixed. Two related findings remain:

- **P2 — Clearing the entire fallback can still leave it enabled.** `internal/api/handlers/management/routing_providers_write.go:79` builds the legacy view from the stored fallback. With `target: codex`, valid mappings, and no stored legacy block, a PATCH sending `providers.claude.fallback: null` and `claude-fallback: {}` leaves both blocks empty after the merge. Load would resolve that to `off`, but conversion restores the old target and mappings and returns `200`. When the PATCH explicitly clears the modern block, convert the merged legacy block rather than rebuilding it from the value just cleared.
- **P2 — Unknown fields in the legacy block still bypass strict validation.** `internal/api/handlers/management/routing_providers_write.go:86` decodes the legacy block permissively before deleting it. With an enabled legacy fallback next to an empty modern node, a PATCH sending the empty modern node and `claude-fallback: {"enabeld":false}` returns `200` and leaves fallback enabled. Strictly validate the submitted legacy block before conversion so the typo returns `400 invalid_config` without changing the file.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the Codex findings with technical rigor.Reading the write flow and validation to check both findings before changing anything.Both findings look real on a first read. Next I'll write failing tests for them, then fix the conversion. First, the docs section that describes these rules:Writing the regression tests first: one for clearing the modern node next to an empty legacy patch, and one for misspelled legacy fields across PATCH/PUT shapes.Package tests pass. Next, the docs: the legacy rules need to cover the merged-block conversion and the rejection of unknown legacy fields.Docs updated. Now running the full verification: gofmt, vet, build, the whole test suite, and race on the management package.Both Codex findings were real and both are fixed. Nothing is committed and there's no signal, so the loop can run Codex again on the fixes.

Finding 1 (P2, clearing the whole fallback left it enabled): valid.
- Cause: when a PATCH named providers.claude.fallback and the merged node came out empty, the legacy patch was still applied over a view built from the stored fallback, which is the value the PATCH had just cleared. So {"providers":{"claude":{"fallback":null}},"claude-fallback":{}} over a stored target: codex node brought back codex and its mappings and returned 200. Load would have read that file as off.
- Fix (routing_providers_write.go, new convertLegacyClaudeFallback): when the write names the modern node, the code now converts the merged legacy block, which is what load reads. The stored fallback isn't used in that case, so a stored api target is not kept either. The client cleared the node on purpose; the api rule exists only for older panels that never send the modern node.
- When the write doesn't name the modern node (the older-panel path), nothing changes: a partial {"enabled": false} still keeps the stored mode and mappings, and a disabled block still keeps a stored api target.
- New tests: a stored codex or api node, cleared with either a null node or all-null fields, next to claude-fallback: {}. The saved file loads as target off with no mappings, and the legacy node is gone.

Finding 2 (P2, unknown legacy fields skipped strict validation): valid.
- Cause: the conversion decoded the legacy block loosely, ignored a typo like enabeld, and then deleted the legacy node, so ValidateV8Config never saw it. This happened on more paths than the one Codex reported: legacy-only PATCHes (shared and key profile), PUT of the whole block, PUT of a single field, and misspelled mapping fields.
- Fix: the submitted legacy block is now decoded strictly through decodeConfigV8Strict, a helper pulled out of unsetClaudeFallback so both checks use the same rule. If it fails, the profile is left as merged and ValidateV8Config rejects the write with 400 invalid_config naming the field, with the file unchanged.
- New tests cover six typo shapes: both blocks in one PATCH, legacy-only shared and key PATCHes, a mapping field, a PUT of the block, and a PUT of one field.
- One mapping case was changed. A mapping with "too" instead of "to" was already rejected with 422 by ParseConfigBytes, because "to" was missing, before strict validation runs. The test now keeps "to" and adds a misspelled "tto", so it actually checks strict decoding.

Docs: docs/management-api-v8.md now says that an unknown field in a legacy block is rejected and the file left unchanged. It also says that a write which names and clears providers.claude.fallback converts the merged legacy block, so null plus {} turns the fallback off.

Verification: all 10 new cases failed before the fix and pass now. gofmt -l and go vet ./... are clean, the build passes, go test ./... passes, and go test -race on the management package passes. Nothing went to the backlog.

Insight: yaml.v3's Node.Decode has no way to reject unknown fields. Strict decoding needs a marshal and then a Decoder with KnownFields(true), which is why the earlier strictness fix stopped at the modern node. With the helper shared, both blocks follow the same rule as ValidateV8Config.
```
#### Iteration 3
- truncated: false
Reviewer output:
```
The explicit-clear fix is correct for the reported case, and legacy-only writes now reject misspelled fields. Two findings remain:

- **P2 — A submitted legacy typo still bypasses validation when the modern fallback wins.** `internal/api/handlers/management/routing_providers_write.go:70` checks the legacy fields only when conversion runs. With a stored `target: codex` and valid mappings, PATCHing `/v8/management/config/routing` with `{"providers":{"claude":{"fallback":{}}},"claude-fallback":{"enabeld":false}}` leaves the modern node nonempty. The handler skips validation, deletes the legacy block, and returns `200` with fallback still enabled. Strictly validate any submitted legacy block before deciding whether to convert or discard it.
- **P3 — The documented clear example can switch fallback to Codex instead of turning it off.** `docs/management-api-v8.md:845` says that sending a null modern fallback and `claude-fallback: {}` turns a stored `api` or `codex` target off. If the file also contains an enabled legacy block with valid mappings, the empty PATCH preserves those fields, and conversion selects `codex`. Qualify the example: it turns fallback off when the merged legacy block is empty or disabled.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the Codex findings with technical rigor.Now extending the typo test to also run against a stored non-zero node (the reviewer's scenario), and adding a test that pins the P3 behaviour.Now wrapping the typo table in a loop over a zero and a non-zero stored node.My added PUT case had an invalid body (codex target needs a mapping); fixing it, then confirming the reviewer's scenario fails without the code fix.The three new cases (including Codex's exact request) fail on the old code and pass with the fix. Now the docs.Now the full verification: format, vet, build, the whole test suite, and race on the package.Both findings were valid, and I fixed both. Nothing is committed and no backlog entry was filed.

P2: valid, fixed.
- I reproduced it as described. With a stored non-zero providers.claude.fallback (for example target codex with mappings), the request {"providers":{"claude":{"fallback":{}}},"claude-fallback":{"enabeld":false}} leaves the merged node non-zero. So convertsLegacyClaudeFallback returned false and the strict check never ran. dropsLegacyClaudeFallback then deleted the legacy block, misspelled field and all, before ValidateV8Config could see it, and the write returned 200. A PUT of routing that names a set node next to a misspelled legacy block took the same path.
- Fix in internal/api/handlers/management/routing_providers_write.go (mergeLegacyClaudeFallbackProfile): any submitted claude-fallback block is now strict-decoded first, before the code decides whether to convert or drop it. If the decode fails, the profile is left as merged. A PATCH never removes the misspelled key from the merged block (null is stored as a value, not a delete), so ValidateV8Config always sees it and rejects the write.
- Only the written block is checked, never the stored one. A hand-edited stored legacy block with a bad field is still dropped once a non-zero modern node wins over it, so ordinary routing writes such as a Codex speed change keep working.
- Tests in routing_providers_write_test.go: the misspelled-field table now runs every case against both a zero and a non-zero stored node, plus a new case for a PUT of a set node next to a misspelled legacy block. Three cases fail on the old code, including the exact request from the finding, and all pass with the fix. Each one checks that the file is unchanged.

P3: valid, fixed in the doc.
- The code is right. After the write the modern node is unset, so load would read the merged legacy block. A PATCH of claude-fallback: {} keeps a stored enabled block with mappings, so converting it to target codex matches what load produces. The doc example claimed the fallback always goes off, which is too broad.
- docs/management-api-v8.md now says the example turns the fallback off only when the merged legacy block is empty or disabled (for example, when the file holds no legacy block). It also says a stored enabled block with mappings converts to target codex with its mode and mappings.
- A new test pins this: a stored codex or api node next to a stored enabled legacy block with mode auto, then the PATCH, should give target codex with the legacy mode and mapping.
- I also rewrote the paragraph above it. It now states up front that a misspelled legacy field is rejected even when a non-zero modern node wins. The earlier edit had also cut the sentence that introduces the rules list in two, so I fixed that.

Verification: gofmt -l, go vet ./... and the build are clean. go test ./... and go test -race on the management package pass.

Insight:
- The bug came from the order of decisions. Whether a block was converted decided whether it was validated, so any path that skipped conversion also skipped validation. Validating before deciding closes every such path at once.
- Validating only the submitted block, not the merged one, is deliberate. Checking the merged block would also catch stored junk from hand edits, and would then block every routing write that used to clean that junk up.
```
#### Iteration 4
- truncated: false
Reviewer output:
```
- **P3 — The `target: null` example overstates when the modern fallback wins.** `docs/management-api-v8.md:830` says clearing `target` on a stored non-zero node prevents legacy conversion. If the stored node is just `{"target":"off"}`, clearing `target` leaves it empty. When the same PATCH sends `claude-fallback: {}` over a stored enabled legacy block with mappings, the effective target becomes `codex`. Qualify the example: the modern node wins only if a non-empty `mode` or `mappings` remains.

Both previous findings are fixed: submitted legacy fields are checked before the block can be dropped, and the separate `fallback: null` example now correctly distinguishes enabled legacy state from empty or disabled state.

```
Evaluator response:
```
Now the regression test for the target-only case, placed right after the existing "PATCH clearing the target of a stored node" subtest.The new test passes, so the behavior Codex described is real: the effective target becomes codex. Now the full suite, vet and build.I fixed this finding.

The P3 finding is valid. In convertsLegacyClaudeFallback (routing_providers_write.go), a write that names providers.claude.fallback converts the legacy block only when the merged node is empty. A stored node that holds just a target, such as {target: off}, becomes empty once the PATCH clears target. The merged legacy block is then converted. If the file holds an enabled legacy block with mappings and the PATCH sends claude-fallback: {}, that block survives the merge and the effective target becomes codex, as Codex described. The doc sentence was too broad, and so was the code comment.

Changes:
- docs/management-api-v8.md: the target: null example now says the modern node wins only when a non-empty mode or mappings remains. Over a target-only node such as {"target": "off"}, the merged node becomes empty and the merged legacy block is converted under the rules below.
- routing_providers_write.go: the convertsLegacyClaudeFallback comment now makes the same distinction. The code itself is unchanged.
- routing_providers_write_test.go: added the subtest "PATCH clearing the target of a stored target-only node". The stored file has {target: off} next to an enabled legacy block with mappings. The test sends PATCH target: null with claude-fallback: {} and checks that the shared profile converts to target codex with the legacy mappings and that the shared legacy node is dropped. It also checks that the key profile, which the write does not name, keeps its legacy node and its off target.

Verification: gofmt and go vet are clean, the build succeeds, and go test ./... passes with no failures.

Insight:
- This is a "judged by the merged node" rule: whether the modern fallback beats the legacy block depends on what is left after the PATCH, not on the stored node. Any field-level null can tip a node into empty. The doc sentence has to describe what remains after the patch, not what was stored.

Nothing is committed and I'm not giving the done signal, because a fix was made and the next Codex pass should check it.
```
#### Iteration 5
- truncated: false
Reviewer output:
```
NO ISSUES FOUND

The previous finding is resolved. `docs/management-api-v8.md:830` now distinguishes clearing a target-only node from clearing a target when mode or mappings remain. The regression test at `internal/api/handlers/management/routing_providers_write_test.go:545` checks that the target-only case restores the enabled legacy fallback and leaves the untouched key profile off.

```
Evaluator response:
```
Codex reported no issues, so I'll check the uncommitted tree, confirm it all came from this review loop, and commit.Codex found no issues this round. The earlier finding was about how the docs described clearing a fallback target, and it is resolved: the docs now separate the two cases, and a regression test covers the target-only one.

What I checked before committing:
- All four dirty files came from earlier rounds of this review loop: docs/management-api-v8.md, routing_providers_write.go, routing_providers_write_test.go and config_v8_etag_test.go. There were no untracked files and no backlog entries.
- gofmt reported nothing, the server builds, go vet passes on the management package, and the management and config package tests pass.
- I staged those four files by name and committed them as ad33115f "fix: address codex review findings". The working tree is now clean.

Insight:
- The main fix decodes strictly, rejecting unknown fields the same way ValidateV8Config does. Before, a misspelled legacy field such as {"enabeld": false} was quietly ignored during the conversion, or dropped along with the legacy block. Now the write is rejected and the file is left unchanged.
- A write that names providers.claude.fallback is now judged by the merged node, not the raw write. A PATCH of {"target": null} over a stored node that still has a mode or mappings keeps those fields. Only when the merged node ends up empty is the legacy block converted, the same way load would read it.

<<<RALPHEX:EXTERNAL_REVIEW_DONE>>>
```

## Validation
- none
- duration_ms: 0
- runs: 0