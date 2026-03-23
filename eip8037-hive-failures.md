# EIP-8037 Hive Failures on `bal-devnet-3` (bal@v5.5.1)

8 failures out of 1054 tests (99.2% pass rate) on `consume-engine` against `erigon v3.5.0-dev-b1564cbd`.

Query with clive:
```bash
clive tests bal-quick --client=erigon --status=fail
clive detail bal-quick --client=erigon --filter="<test_name>"
```

---

## Bug 1: Code deposit charges state gas before regular gas

**Failing test:** `test_max_code_size_deposit_gas[short_one_gas]`
**Error:** `gas used by execution: 38,601,120, in header: 16,777,216`

**Root cause:** In `evm.go:586-601`, code deposit charges state gas (cpsb × len) **before** regular gas (6 × ceil(len/32)). EIP-8037 requires regular gas to be charged first ([EIPs#11421](https://github.com/ethereum/EIPs/pull/11421)). When the reservoir is insufficient, the state gas spill inflates `blockState`, producing a `gas_used` of ~38.6M instead of the expected ~16.7M.

**Current code:**
```go
// evm.go:589-592
createDataGas = uint64(len(ret)) * evm.Context.CostPerStateByte  // state gas
gasRemaining, stateGasOk = useMdGas(evm, gasRemaining, createDataGas, mdgas.StateGas, ...)
if stateGasOk {
    createDataGas = 6 * ((uint64(len(ret)) + 31) / 32)  // regular gas
}
```

**Fix:** Swap the order — charge regular hash gas first, then state gas:
```go
createDataGas = 6 * ((uint64(len(ret)) + 31) / 32)  // regular gas first
gasRemaining, regularGasOk = useMdGas(evm, gasRemaining, createDataGas, mdgas.RegularGas, ...)
if regularGasOk {
    createDataGas = uint64(len(ret)) * evm.Context.CostPerStateByte  // state gas second
    gasRemaining, stateGasOk = useMdGas(evm, gasRemaining, createDataGas, mdgas.StateGas, ...)
}
```

**Spec reference:** `interpreter.py:226-239` — `charge_gas(evm, code_hash_gas)` then `charge_state_gas(evm, code_deposit_state_gas)`.

---

## Bug 2: BAL hash mismatch from gas ordering difference

**Failing test:** `test_sstore_oog_reservoir_inflation_detection`
**Error:** `block access list mismatch: got 0x86befd90... expected 0x4187cec6...`

**Root cause:** This is **not** a direct gas ordering bug in the interpreter — the regular-before-state ordering in `interpreter.go:456-476` is correct. However, some other code path (likely the code deposit ordering from Bug 1 or a related CREATE/CALL gas path) produces a different execution trace, which changes which storage slots and addresses are accessed, producing a different BAL hash.

**Investigation needed:** Run the test with tracing to compare the exact execution steps between erigon and the reference spec. The BAL mismatch means the set of accessed accounts/slots differs, not just gas values.

---

## Bug 3: Block gas pool accounting for TX_MAX_GAS_LIMIT

**Failing tests:**
- `test_block_regular_gas_limit[exceed=True]` — `block gas used overflow`
- `test_block_state_gas_limit[exceed=True]` — `gas used: 49,212, in header: 37,568`

**Root cause:** The block gas pool (`state_transition.go:648`) returns unused gas via:
```go
st.gp.AddGas(st.initialGas.Total() - max(st.blockRegularGasUsed, st.blockStateGasUsed))
```

For `test_block_regular_gas_limit`: Multiple transactions at TX_MAX_GAS_LIMIT fill the block. The test expects the last tx to be rejected as `GAS_ALLOWANCE_EXCEEDED`, but erigon rejects the entire block with `block gas used overflow` — it's computing block gas incorrectly, possibly double-counting state gas or not accounting for the 2D split properly when deducting from the pool.

For `test_block_state_gas_limit`: A single SSTORE tx at high gas limit. Expected gas_used=37,568 (= 32×1174 = pure state gas). Erigon computes 49,212 — an extra 11,644 in regular gas that shouldn't be there.

**Spec reference:** `fork.py:1141-1144` — `block_gas_used = max(sum_regular, sum_state)` computed at block level.

---

## Bug 4: TX_MAX_GAS_LIMIT rejection error mapping

**Failing tests:**
- `test_calldata_floor_exceeding_tx_gas_limit_cap[exceeds_cap]` — `gas limit too high` instead of `INTRINSIC_GAS_TOO_LOW`
- `test_tx_gas_above_cap_at_transition[above_cap]` — `gas limit too high` instead of `GAS_LIMIT_EXCEEDS_MAXIMUM`
- `test_intrinsic_regular_gas_exceeds_cap` — `gas limit too high` instead of `INTRINSIC_GAS_TOO_LOW`

**Root cause:** In `state_transition.go:341-344`, the EIP-7825 cap check returns a single generic error:
```go
return fmt.Errorf("%w: address %v, gas limit %d", ErrGasLimitTooHigh, from, capGas)
```

The test framework expects distinct error types depending on the failure mode:
- `GAS_LIMIT_EXCEEDS_MAXIMUM` when `tx.gas > TX_MAX_GAS_LIMIT`
- `INTRINSIC_GAS_TOO_LOW` when intrinsic regular gas exceeds the cap (e.g., huge calldata)

**Fix:** Differentiate the error based on what triggered the cap violation. If `tx.gas > TX_MAX_GAS_LIMIT`, return a "gas limit exceeds maximum" error. If intrinsic regular gas (including calldata floor) exceeds the cap, return an "intrinsic gas too low" style error.

**Spec reference:** `fork.py:1013-1018` — the cap is `min(TX_MAX_GAS_LIMIT, tx.gas) - intrinsic_regular`, validated before execution.

---

## Bug 5: BAL size limit off-by-one

**Failing test:** `test_bal_gas_limit_boundary[below_boundary]`
**Error:** `block access list too large: 15 items > 14 max (gas limit 29999 / 2000)`

**Root cause:** In `block_access_list.go:862-873`, the BAL item count includes `StorageReads`:
```go
items++ // address
items += uint64(len(ac.StorageChanges))
items += uint64(len(ac.StorageReads))
```

The spec (EIP-7928) defines `bal_items = count(addresses) + count(storage_keys)` where storage keys are the union of changed and read slots. If erigon counts changes + reads separately (double-counting slots that appear in both), the item count is inflated. Alternatively, the test expects 15 items to fit within `29999/2000 = 14` — meaning the spec allows `<=` not `<`, or the item counting methodology differs.

**Fix:** Deduplicate slots using a map before counting. Committed as `e2f98634`.

**Spec reference:** `block_access_lists.py:756-763` — uses `set()` to collect unique slots across changes and reads.

---

## Summary

| # | Test | Root Cause | Fix |
|---|------|-----------|-----|
| 1 | `test_max_code_size_deposit_gas` | Code deposit: state gas before regular gas | ✅ `8deb63d6` |
| 2 | `test_sstore_oog_reservoir_inflation` | BAL hash mismatch — parallel executor | ⚠️ Likely [#20042](https://github.com/erigontech/erigon/pull/20042) |
| 3 | `test_block_regular_gas_limit` | Parallel executor: gas pool not checked between txs | ⚠️ Parallel executor |
| 4 | `test_block_state_gas_limit` | Parallel executor: gas pool not checked between txs | ⚠️ Parallel executor |
| 5-7 | `test_*_cap_*` (×3) | Generic error instead of specific types | ✅ `f9c254c1` |
| 8 | `test_bal_gas_limit_boundary` | BAL item count double-counting shared slots | ✅ `e2f98634` |

### Fixes applied (branch: `bal-devnet-3-fixes`)

1. **`8deb63d6`** — `execution/vm: fix EIP-8037 code deposit gas charge ordering`
2. **`e2f98634`** — `execution/types: fix BAL item count double-counting shared slots`
3. **`f9c254c1`** — `execution/protocol: distinguish EIP-7825 cap errors for EEST mapping`

### Remaining (parallel executor infrastructure)

Bugs 2-4 are caused by erigon's parallel executor (`Exec3Parallel=true`) not correctly handling the 2D gas pool between speculative tx executions. The parallel executor processes both txs concurrently, then validates — but the gas pool check between txs is skipped during speculation.

Related open PRs from yperbasis:
- [#20041](https://github.com/erigontech/erigon/pull/20041) — Fix parallel executor dropping last receipt from cache
- [#20042](https://github.com/erigontech/erigon/pull/20042) — Use finalizeWithIBS only for Amsterdam blocks (fixes wrong trie roots during reorgs)

Bug 2 (BAL hash mismatch) may also resolve once [#20042](https://github.com/erigontech/erigon/pull/20042) lands, as it changes the parallel executor's BAL finalization path.

**References:**
- [EIP-8037](https://eips.ethereum.org/EIPS/eip-8037) — State Creation Gas Cost Increase
- [EIPs#11421](https://github.com/ethereum/EIPs/pull/11421) — Regular gas before state gas ordering
- [EIPs#11414](https://github.com/ethereum/EIPs/pull/11414) — Two-phase gas validation
- [bal@v5.5.1 fixtures](https://github.com/ethereum/execution-spec-tests/releases/tag/bal%40v5.5.1)
