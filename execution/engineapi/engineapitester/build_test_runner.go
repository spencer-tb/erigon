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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/common/hexutil"
	"github.com/erigontech/erigon/execution/engineapi"
	"github.com/erigontech/erigon/execution/engineapi/engine_block_downloader"
	enginetypes "github.com/erigontech/erigon/execution/engineapi/engine_types"
	"github.com/erigontech/erigon/execution/execmodule/chainreader"
	"github.com/erigontech/erigon/execution/execmodule/execmoduletester"
	"github.com/erigontech/erigon/execution/p2p"
	"github.com/erigontech/erigon/node/ethconfig"
	"github.com/erigontech/erigon/rpc"
)

// A BuildTest is a blockchain_tests_engine fixture whose valid payloads are
// built again by the block builder, through testing_buildBlockV1, and compared
// with the fixture's before the fixture's payload is imported.
type BuildTest struct {
	EngineTest
	// Built is the number of payloads the builder produced.
	Built int
	// Mismatches lists every field of a built payload that differs from the
	// fixture's payload, in fixture order.
	Mismatches []Mismatch
}

// A Mismatch is a field of a built payload that differs from the fixture's,
// with both values as they appear in the payload's JSON.
type Mismatch struct {
	Index    int             `json:"index"` // position in the fixture's engineNewPayloads
	Field    string          `json:"field"`
	Expected json.RawMessage `json:"expected"`
	Got      json.RawMessage `json:"got"`
}

// BuildResult returns how many payloads the builder produced and every
// mismatch found.
func (bt *BuildTest) BuildResult() (int, []Mismatch) {
	return bt.Built, bt.Mismatches
}

// RunCLI builds and imports the fixture's valid payloads in process, on a fresh
// execution module. Payloads the fixture expects to be invalid are skipped.
func (bt *BuildTest) RunCLI() error {
	bt.Built, bt.Mismatches = 0, []Mismatch{}
	if err := bt.RunCLIWith(bt.deliverBuilt); err != nil {
		return err
	}
	if len(bt.Mismatches) > 0 || len(bt.Rejections) > 0 {
		return fmt.Errorf("%d mismatched fields, %d refused payloads", len(bt.Mismatches), len(bt.Rejections))
	}
	return nil
}

// deliverBuilt builds every valid payload on its parent through the testing
// API and then imports the fixture's payload, not the built one, through the
// engine API, so the chain follows the fixture whatever the builder produced.
func (bt *BuildTest) deliverBuilt(m *execmoduletester.ExecModuleTester) error {
	// Forkchoice work outlives its response; drain it before the tester closes.
	defer m.ExecModule.Drain()
	// With no peers, a payload whose parent is unknown is answered SYNCING.
	noPeers := p2p.NewBackwardBlockDownloader(m.Log, nil, p2p.NewPeerTracker(m.Log, nil), m.Dirs.Tmp)
	srv := engineapi.NewEngineServer(
		m.Log,
		m.ChainConfig,
		m.ExecModule,
		engine_block_downloader.NewEngineBlockDownloader(m.Ctx, m.Log, m.ExecModule, m.BlockReader, m.DB, m.ChainConfig, ethconfig.Defaults.Sync, noPeers),
		false, /* caplin */
		false, /* internalCL */
		true,  /* proposing */
		true,  /* consuming */
		nil,   /* txPool */
		nil,   /* blobGetter */
		ethconfig.Defaults.FcuTimeout,
		ethconfig.Defaults.MaxReorgDepth,
	)
	rpcSrv := rpc.NewServer(1, false, false, false, m.Log, 0)
	defer rpcSrv.Stop()
	for _, api := range []rpc.API{srv.API(), engineapi.NewTestingRPCEntry(srv)} {
		if err := rpcSrv.RegisterAPI(api); err != nil {
			return err
		}
	}
	client := rpc.DialInProc(rpcSrv, m.Log)
	defer client.Close()
	if len(bt.payloads) > 0 {
		if err := forkchoiceUpdated(m.Ctx, client, m.Genesis.Hash(), bt.payloads[0].FcuVersion, bt.SyncingTimeout); err != nil {
			return fmt.Errorf("forkchoice update to genesis: %w", err)
		}
	}
	chain := chainreader.NewChainReaderEth1(m.ChainConfig, m.ExecModule, 0)
	for i, payload := range bt.payloads {
		if payload.ValidationError != "" || payload.ErrorCode != "" {
			continue
		}
		if err := bt.build(m, client, i, payload); err != nil {
			return fmt.Errorf("payload %d: %w", i, err)
		}
		if err := bt.sendPayload(m.Ctx, client, chain, i, payload); err != nil {
			return fmt.Errorf("payload %d: %w", i, err)
		}
	}
	return nil
}

// build builds payload p's block through testing_buildBlockV1 and records every
// field in which it differs from the fixture's payload. A build the client
// refuses is a rejection.
func (bt *BuildTest) build(m *execmoduletester.ExecModuleTester, client *rpc.Client, index int, p EngineXTestNewPayload) error {
	if len(p.Params) == 0 {
		return errors.New("payload has no params")
	}
	var want enginetypes.ExecutionPayload
	if err := json.Unmarshal(p.Params[0], &want); err != nil {
		return err
	}
	newPayloadVersion, err := strconv.Atoi(p.NewPayloadVersion)
	if err != nil {
		return fmt.Errorf("newPayloadVersion %q: %w", p.NewPayloadVersion, err)
	}
	fcuVersion, err := strconv.Atoi(p.FcuVersion)
	if err != nil {
		return fmt.Errorf("forkchoiceUpdatedVersion %q: %w", p.FcuVersion, err)
	}
	attributes := enginetypes.PayloadAttributes{
		Timestamp:             want.Timestamp,
		PrevRandao:            want.PrevRandao,
		SuggestedFeeRecipient: want.FeeRecipient,
		Withdrawals:           want.Withdrawals,
	}
	// The parent beacon block root is a newPayload param from V3 on.
	if newPayloadVersion >= 3 {
		if len(p.Params) < 3 {
			return errors.New("payload has no parentBeaconBlockRoot param")
		}
		var root common.Hash
		if err := json.Unmarshal(p.Params[2], &root); err != nil {
			return fmt.Errorf("parentBeaconBlockRoot: %w", err)
		}
		attributes.ParentBeaconBlockRoot = &root
	}
	// The slot number and the gas limit target are attributes from
	// forkchoiceUpdatedV4 on. Before, the builder's own gas target is set to
	// the payload's gas limit, which a valid payload reaches in one step.
	if fcuVersion >= 4 {
		gasLimit := want.GasLimit
		attributes.SlotNumber = want.SlotNumber
		attributes.TargetGasLimit = &gasLimit
	}
	m.SetBuilderGasLimit(uint64(want.GasLimit))

	var built enginetypes.GetPayloadResponse
	err = client.CallContext(m.Ctx, &built, "testing_buildBlockV1", want.ParentHash, attributes, want.Transactions, want.ExtraData)
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		bt.Reject(index, nil, rpcErrorString(rpcErr))
		return nil
	}
	if err != nil {
		return err
	}
	bt.Built++

	mismatches, err := fieldMismatches(index, &want, built.ExecutionPayload)
	if err != nil {
		return err
	}
	bt.Mismatches = append(bt.Mismatches, mismatches...)

	// The execution requests are a newPayload param from V4 on.
	if newPayloadVersion >= 4 {
		if len(p.Params) < 4 {
			return errors.New("payload has no executionRequests param")
		}
		var wantRequests []hexutil.Bytes
		if err := json.Unmarshal(p.Params[3], &wantRequests); err != nil {
			return fmt.Errorf("executionRequests: %w", err)
		}
		expected, err := json.Marshal(nonNil(wantRequests))
		if err != nil {
			return err
		}
		got, err := json.Marshal(nonNil(built.ExecutionRequests))
		if err != nil {
			return err
		}
		if !bytes.Equal(expected, got) {
			bt.Mismatches = append(bt.Mismatches, Mismatch{Index: index, Field: "executionRequests", Expected: expected, Got: got})
		}
	}
	return nil
}

// fieldMismatches compares two execution payloads field by field in their JSON
// form, so both values of a mismatch read as they do in a fixture. Fields are
// listed in name order.
func fieldMismatches(index int, want, built *enginetypes.ExecutionPayload) ([]Mismatch, error) {
	wantFields, err := jsonFields(want)
	if err != nil {
		return nil, err
	}
	builtFields, err := jsonFields(built)
	if err != nil {
		return nil, err
	}
	names := maps.Clone(wantFields)
	maps.Copy(names, builtFields)

	var mismatches []Mismatch
	for _, name := range slices.Sorted(maps.Keys(names)) {
		expected, got := orNull(wantFields[name]), orNull(builtFields[name])
		if !bytes.Equal(expected, got) {
			mismatches = append(mismatches, Mismatch{Index: index, Field: name, Expected: expected, Got: got})
		}
	}
	return mismatches, nil
}

// jsonFields returns the top-level fields of v's JSON encoding.
func jsonFields(v any) (map[string]json.RawMessage, error) {
	enc, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(enc, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// orNull returns raw, or JSON null for a field one payload does not have.
func orNull(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return json.RawMessage("null")
	}
	return raw
}

// nonNil returns list, or an empty list for nil, so that no requests and an
// empty request list compare equal.
func nonNil(list []hexutil.Bytes) []hexutil.Bytes {
	if list == nil {
		return []hexutil.Bytes{}
	}
	return list
}
