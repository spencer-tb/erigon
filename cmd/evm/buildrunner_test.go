// Copyright 2026 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/erigontech/erigon/cmd/utils/cmdtest"
	"github.com/erigontech/erigon/execution/engineapi/engineapitester"
)

// runBuildTestCmd runs buildtest on the fixture file at path, which holds one
// fixture, and returns the run's exit status and the fixture's result.
func runBuildTestCmd(t *testing.T, path string) (int, testResult) {
	t.Helper()
	tt := cmdtest.NewTestCmd(t, nil)
	tt.Run("evm-test", "buildtest", "--jsonout", path)
	stdout := tt.Output()
	tt.WaitExit()
	var results []testResult
	require.NoError(t, json.Unmarshal(stdout, &results), string(stdout))
	require.Len(t, results, 1)
	require.Contains(t, string(stdout), `"mismatches": [`, "the result has no mismatches list")
	return tt.ExitStatus(), results[0]
}

// buildtest builds every payload a fixture expects to be valid, skips the ones
// it expects to be invalid, and reports how many it built next to empty
// mismatch and rejection lists.
func TestBuildTestBuildsValidPayloads(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}
	for _, tc := range []struct {
		name, fixture string
		built         int
	}{
		{"valid", "../../execution/tests/testdata/engine/bal_cross_block_ripemd160_state_leak.json", 2},
		// The block's blob transaction comes without its sidecar, as payloads
		// carry it, and the block is still built.
		{"blob", "testdata/buildtest_blob.json", 1},
		// The fixture's only payload is expected to be invalid.
		{"invalid", "../../execution/tests/testdata/engine/bal_invalid_balance_value.json", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, res := runBuildTestCmd(t, tc.fixture)
			require.Equal(t, 0, status, res.Error)
			require.True(t, res.Pass, res.Error)
			require.NotNil(t, res.Built)
			require.Equal(t, tc.built, *res.Built)
			require.Empty(t, res.Mismatches)
			require.Empty(t, res.Rejections)
		})
	}
}

// A field in which the built payload differs from the fixture's is reported
// with both values, the fixture's payload is still imported, and the run exits
// 1.
func TestBuildTestReportsMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}
	wrongRoot := "0x" + strings.Repeat("00", 31) + "01"
	var builtRoot string
	path := writeEditedFixture(t, "../../execution/tests/testdata/engine/bal_cross_block_ripemd160_state_leak.json", func(fixture map[string]any) {
		params := enginePayloadParams(fixture, 0)
		builtRoot = params["stateRoot"].(string)
		params["stateRoot"] = wrongRoot
	})
	status, res := runBuildTestCmd(t, path)
	require.Equal(t, exitFailedRun, status)
	require.False(t, res.Pass)
	require.NotNil(t, res.Built)
	require.Equal(t, 1, *res.Built)
	require.Equal(t, []engineapitester.Mismatch{{
		Index:    0,
		Field:    "stateRoot",
		Expected: json.RawMessage(`"` + wrongRoot + `"`),
		Got:      json.RawMessage(`"` + builtRoot + `"`),
	}}, res.Mismatches)
	// The fixture's payload no longer matches its own block hash, so the client
	// refuses to import it and the fixture stops there.
	require.Len(t, res.Rejections, 1)
	require.Equal(t, 0, res.Rejections[0].Index)
}
