// Copyright 2022 Woodpecker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package log

import (
	"context"
	"fmt"
	"strconv"

	"github.com/urfave/cli/v3"

	"go.woodpecker-ci.org/woodpecker/v2/cli/internal"
)

var logPurgeCmd = &cli.Command{
	Name:      "purge",
	Usage:     "purge a log",
	ArgsUsage: "<repo-id|repo-full-name> <pipeline> [step]",
	Action:    logPurge,
}

func logPurge(ctx context.Context, c *cli.Command) (err error) {
	client, err := internal.NewClient(ctx, c)
	if err != nil {
		return err
	}
	repoIDOrFullName := c.Args().First()
	repoID, err := internal.ParseRepo(client, repoIDOrFullName)
	if err != nil {
		return err
	}
	number, err := strconv.ParseInt(c.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}

	stepArg := c.Args().Get(2) //nolint:mnd
	// TODO: Add lookup by name: stepID, err := internal.ParseStep(client, repoID, stepIDOrName)
	var stepID int64
	if len(stepArg) != 0 {
		stepID, err = strconv.ParseInt(stepArg, 10, 64)
		if err != nil {
			return err
		}
	}

	if stepID > 0 {
		err = client.StepLogsPurge(repoID, number, stepID)
	} else {
		err = client.LogsPurge(repoID, number)
	}
	if err != nil {
		return err
	}

	fmt.Printf("Purging logs for pipeline %s#%d\n", repoIDOrFullName, number)
	return nil
}
