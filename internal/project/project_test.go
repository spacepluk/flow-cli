/*
 * Flow CLI
 *
 * Copyright 2019 Dapper Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package project

import (
	"github.com/onflow/flow-cli/internal/util"
	"github.com/onflow/flow-cli/pkg/flowkit"
	"github.com/onflow/flow-cli/pkg/flowkit/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func Test_ProjectDeploy(t *testing.T) {
	srv, state, rw := util.TestMocks(t)

	t.Run("Fail contract errors", func(t *testing.T) {
		srv.DeployProject.Return(nil, &flowkit.ProjectDeploymentError{})
		_, err := deploy([]string{}, util.NoFlags, util.NoLogger, srv.Mock, state)
		assert.EqualError(t, err, "failed deploying all contracts")
	})

	t.Run("Success replace standard contracts", func(t *testing.T) {
		const ft = "FungibleToken"
		state.Contracts().AddOrUpdate(config.Contract{
			Name:     ft,
			Location: "./ft.cdc",
		})
		_ = rw.WriteFile("./ft.cdc", []byte("test"), 0677) // mock the file

		state.Deployments().AddContract(
			config.DefaultEmulatorServiceAccountName,
			config.MainnetNetwork.Name,
			config.ContractDeployment{Name: "FungibleToken"},
		)

		err := checkForStandardContractUsageOnMainnet(state, util.NoLogger, true)
		require.NoError(t, err)

		assert.Len(t, state.Deployments().ByNetwork(config.EmulatorNetwork.Name), 0) // should remove it
		assert.NotNil(t, state.Contracts().ByName(ft).Aliases)
		assert.Equal(t, "f233dcee88fe0abe", state.Contracts().ByName(ft).Aliases.ByNetwork(config.MainnetNetwork.Name).Address.String())
	})

}
