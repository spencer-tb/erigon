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

const (
	buildXFixture  = "testdata/buildx/fixture.json"
	buildXPreAlloc = "testdata/buildx/pre_alloc"
)

// runBuildXTestCmd runs buildxtest on the engine-x fixture file at path, which
// holds one test, and returns the run's exit status and the test's result.
func runBuildXTestCmd(t *testing.T, path string) (int, testResult) {
	t.Helper()
	tt := cmdtest.NewTestCmd(t, nil)
	tt.Run("evm-test", "buildxtest", "--jsonout", "--pre-alloc-dir", buildXPreAlloc, path)
	stdout := tt.Output()
	tt.WaitExit()
	var results []testResult
	require.NoError(t, json.Unmarshal(stdout, &results), string(stdout))
	require.Len(t, results, 1)
	require.Contains(t, string(stdout), `"mismatches": [`, "the result has no mismatches list")
	return tt.ExitStatus(), results[0]
}

// buildxtest builds a test's valid payloads on the node of its pre-alloc
// group and compares them with the fixture's.
func TestBuildXTestBuildsValidPayloads(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}
	status, res := runBuildXTestCmd(t, buildXFixture)
	require.Equal(t, 0, status, res.Error)
	require.True(t, res.Pass, res.Error)
	require.NotNil(t, res.Built)
	require.Equal(t, 2, *res.Built)
	require.Empty(t, res.Mismatches)
	require.Empty(t, res.Rejections)
}

// A field in which a built payload differs from the fixture's fails the test
// with both values reported, and the run exits 1.
func TestBuildXTestReportsMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}
	wrongRoot := "0x" + strings.Repeat("00", 31) + "01"
	var builtRoot string
	path := writeEditedFixture(t, buildXFixture, func(fixture map[string]any) {
		params := enginePayloadParams(fixture, 0)
		builtRoot = params["stateRoot"].(string)
		params["stateRoot"] = wrongRoot
	})
	status, res := runBuildXTestCmd(t, path)
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
	// The fixture's payload no longer matches its own block hash, so the node
	// refuses to import it and the test stops there.
	require.Len(t, res.Rejections, 1)
	require.Equal(t, 0, res.Rejections[0].Index)
}
