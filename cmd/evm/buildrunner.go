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
	"context"
	"errors"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/execution/engineapi/engineapitester"
	"github.com/erigontech/erigon/execution/tests/testutil"
)

var buildTestCommand = cli.Command{
	Action:    buildTestCmd,
	Name:      "buildtest",
	Usage:     "Builds the valid payloads of the given engine API tests (blockchain_tests_engine fixtures) with the block builder and compares them with the fixtures",
	ArgsUsage: "<path>...",
	Flags: []cli.Flag{
		&JSONOutputFlag,
		&RunFlag,
		&ExcludeFlag,
		&SyncingTimeoutFlag,
		&VerbosityFlag,
		&WorkersFlag,
	},
}

func buildTestCmd(_ context.Context, ctx *cli.Command) error {
	if ctx.Int(VerbosityFlag.Name) > 0 {
		log.Root().SetHandler(log.LvlFilterHandler(log.Lvl(ctx.Int(VerbosityFlag.Name)), log.StderrHandler))
	} else {
		log.Root().SetHandler(log.LvlFilterHandler(log.LvlError, log.StderrHandler))
	}

	if !ctx.Args().Present() {
		return exitIncomplete(errors.New("path argument required"))
	}
	workers := ctx.Uint64(WorkersFlag.Name)
	if workers == 0 {
		return exitIncomplete(fmt.Errorf("--%s must be >= 1", WorkersFlag.Name))
	}
	if ctx.Duration(SyncingTimeoutFlag.Name) <= 0 {
		return exitIncomplete(fmt.Errorf("--%s must be positive", SyncingTimeoutFlag.Name))
	}
	filter, err := compileTestFilter(ctx.String(RunFlag.Name), ctx.StringSlice(ExcludeFlag.Name))
	if err != nil {
		return exitIncomplete(err)
	}

	files, err := collectArgFiles(ctx, filter)
	if err != nil {
		return exitIncomplete(err)
	}
	var incomplete incompleteRun
	results := incomplete.run(files, workers, func(fname string) ([]testResult, error) {
		return runBuildTest(ctx, fname, filter)
	})
	report(ctx, results)
	return incomplete.status(anyFailed(results))
}

// runBuildTest runs the build tests in fname. No execution paths are reported:
// the builder derives each access list itself, so the BAL flags do not apply.
func runBuildTest(ctx *cli.Command, fname string, filter testFilter) ([]testResult, error) {
	return runFixtures(ctx, fname, filter, nil, func() (fixture, *testutil.BlockTest) {
		test := &engineapitester.BuildTest{EngineTest: engineapitester.EngineTest{SyncingTimeout: ctx.Duration(SyncingTimeoutFlag.Name)}}
		return test, &test.BlockTest
	})
}
