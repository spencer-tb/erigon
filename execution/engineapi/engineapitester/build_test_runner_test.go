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

package engineapitester

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	enginetypes "github.com/erigontech/erigon/execution/engineapi/engine_types"
	"github.com/erigontech/erigon/rpc"
)

const buildTestPayload = `{"parentHash":"0x0000000000000000000000000000000000000000000000000000000000000001","feeRecipient":"0x0000000000000000000000000000000000000000","timestamp":"0xc","prevRandao":"0x0000000000000000000000000000000000000000000000000000000000000000","gasLimit":"0x1c9c380","transactions":[],"extraData":"0x"}`

// payloadAt returns a payload sent with newPayload and forkchoiceUpdated at the
// given versions, with the params those versions take.
func payloadAt(newPayloadVersion, fcuVersion string) EngineXTestNewPayload {
	params := []json.RawMessage{json.RawMessage(buildTestPayload), json.RawMessage(`[]`), json.RawMessage(`"0x0000000000000000000000000000000000000000000000000000000000000000"`), json.RawMessage(`[]`)}
	switch newPayloadVersion {
	case "1", "2":
		params = params[:1]
	case "3":
		params = params[:3]
	}
	return EngineXTestNewPayload{Params: params, NewPayloadVersion: newPayloadVersion, FcuVersion: fcuVersion}
}

// A JSON-RPC error from testing_buildBlockV1 is the client refusing the build,
// and any other error is the call failing.
func TestBuildAndCompareTellsARefusalFromAFailedCall(t *testing.T) {
	noGasLimit := func(*uint64) {}
	refuse := func(any, string, ...any) error { return &rpc.CustomError{Code: -32000, Message: "refused"} }
	mismatches, refused, err := buildAndCompare(refuse, noGasLimit, 0, payloadAt("1", "1"))
	require.NoError(t, err)
	require.Empty(t, mismatches)
	require.Equal(t, "-32000: refused", refused)

	fail := func(any, string, ...any) error { return errors.New("connection lost") }
	_, refused, err = buildAndCompare(fail, noGasLimit, 0, payloadAt("1", "1"))
	require.Error(t, err)
	require.Empty(t, refused)
}

// Before forkchoiceUpdatedV4 the builder's own gas target is the payload's gas
// limit. From V4 on it keeps its default, and only the attributes name the
// target.
func TestBuildAndCompareSetsTheGasTargetByFork(t *testing.T) {
	for _, tc := range []struct {
		newPayloadVersion, fcuVersion string
		wantBuilderGasLimit           *uint64
		wantAttributeTarget           bool
	}{
		{"1", "1", ptr(uint64(0x1c9c380)), false},
		{"4", "3", ptr(uint64(0x1c9c380)), false},
		{"5", "4", nil, true},
	} {
		var builderGasLimit *uint64
		setGasLimit := func(gasLimit *uint64) { builderGasLimit = gasLimit }
		var attributes enginetypes.PayloadAttributes
		call := func(_ any, _ string, args ...any) error {
			attributes = args[1].(enginetypes.PayloadAttributes)
			return &rpc.CustomError{Code: -32000, Message: "refused"}
		}
		_, _, err := buildAndCompare(call, setGasLimit, 0, payloadAt(tc.newPayloadVersion, tc.fcuVersion))
		require.NoError(t, err)
		require.Equal(t, tc.wantBuilderGasLimit, builderGasLimit, "newPayloadV%s", tc.newPayloadVersion)
		require.Equal(t, tc.wantAttributeTarget, attributes.TargetGasLimit != nil, "newPayloadV%s", tc.newPayloadVersion)
	}
}

func ptr[T any](v T) *T { return &v }
