// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package c

import (
	"math/big"
	"strings"

	ginkgo "github.com/onsi/ginkgo/v2"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/ava-labs/coreth/core/types"
	"github.com/ava-labs/coreth/params"
	"github.com/ava-labs/coreth/plugin/evm"

	"github.com/ava-labs/avalanchego/tests"
	"github.com/ava-labs/avalanchego/tests/e2e"
	"github.com/ava-labs/avalanchego/tests/fixture/testnet"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
)

// This test uses the compiled bin for `hashing.sol` as
// well as its ABI contained in `hashing_contract.go`.

var _ = e2e.DescribeCChain("[Dynamic Fees]", func() {
	require := require.New(ginkgo.GinkgoT())

	// Need a gas limit much larger than the standard 21_000 to enable
	// the contract to induce a gas price increase
	const largeGasLimit = uint64(8_000_000)

	// TODO(marun) What is the significance of this value?
	gasTip := big.NewInt(1000 * params.GWei)

	ginkgo.It("should ensure that the gas price is affected by load", func() {
		ginkgo.By("creating a new private network to ensure isolation from other tests")
		privateNetwork := e2e.Env.NewPrivateNetwork()

		ginkgo.By("allocating a pre-funded key")
		key := privateNetwork.GetConfig().FundedKeys[0]
		ethAddress := evm.GetEthAddress(key)

		ginkgo.By("initializing a coreth client")
		node := privateNetwork.GetNodes()[0]
		nodeURI := testnet.NodeURI{
			NodeID: node.GetID(),
			URI:    node.GetProcessContext().URI,
		}
		ethClient := e2e.Env.NewEthClient(nodeURI)

		ginkgo.By("initializing a transaction signer")
		cChainID, err := ethClient.ChainID(e2e.DefaultContext())
		require.NoError(err)
		signer := types.NewEIP155Signer(cChainID)
		ecdsaKey := key.ToECDSA()
		sign := func(tx *types.Transaction) *types.Transaction {
			signedTx, err := types.SignTx(tx, signer, ecdsaKey)
			require.NoError(err)
			return signedTx
		}

		var contractAddress common.Address
		ginkgo.By("deploying an expensive contract", func() {
			// Create transaction
			nonce, err := ethClient.AcceptedNonceAt(e2e.DefaultContext(), ethAddress)
			require.NoError(err)
			compiledContract := common.Hex2Bytes(hashingCompiledContract)
			tx := types.NewTx(&types.LegacyTx{
				Nonce:    nonce,
				GasPrice: gasTip,
				Gas:      largeGasLimit,
				Value:    common.Big0,
				Data:     compiledContract,
			})

			// Send the transaction and wait for acceptance
			signedTx := sign(tx)
			receipt := e2e.SendEthTransaction(ethClient, signedTx)

			contractAddress = receipt.ContractAddress
		})

		var gasPrice *big.Int
		ginkgo.By("calling the expensive contract repeatedly until a gas price increase is detected", func() {
			// Evaluate the bytes representation of the contract
			hashingABI, err := abi.JSON(strings.NewReader(hashingABIJson))
			require.NoError(err)
			contractData, err := hashingABI.Pack("hashIt")
			require.NoError(err)

			var initialGasPrice *big.Int
			e2e.Eventually(func() bool {
				// Check the gas price
				var err error
				gasPrice, err = ethClient.SuggestGasPrice(e2e.DefaultContext())
				require.NoError(err)
				if initialGasPrice == nil {
					initialGasPrice = gasPrice
					tests.Outf("{{blue}}initial gas price is %v{{/}}\n", initialGasPrice)
				} else if gasPrice.Cmp(initialGasPrice) > 0 {
					// Gas price has increased
					tests.Outf("{{blue}}gas price has increased to %v{{/}}\n", gasPrice)
					return true
				}

				// Create the transaction
				nonce, err := ethClient.AcceptedNonceAt(e2e.DefaultContext(), ethAddress)
				require.NoError(err)
				tx := types.NewTx(&types.LegacyTx{
					Nonce:    nonce,
					GasPrice: gasTip,
					Gas:      largeGasLimit,
					To:       &contractAddress,
					Value:    common.Big0,
					Data:     contractData,
				})

				// Send the transaction and wait for acceptance
				signedTx := sign(tx)
				_ = e2e.SendEthTransaction(ethClient, signedTx)

				// The gas price will be checked at the start of the next iteration
				return false
			}, e2e.DefaultTimeout, e2e.DefaultPollingInterval, "failed to see gas price increase before timeout")
		})

		ginkgo.By("waiting for the gas price to decrease...", func() {
			initialGasPrice := gasPrice
			e2e.Eventually(func() bool {
				var err error
				gasPrice, err = ethClient.SuggestGasPrice(e2e.DefaultContext())
				require.NoError(err)
				tests.Outf("{{blue}}.{{/}}")
				return initialGasPrice.Cmp(gasPrice) > 0
			}, e2e.DefaultTimeout, e2e.DefaultPollingInterval, "failed to see gas price decrease before timeout")
			tests.Outf("\n{{blue}}gas price has decreased to %v{{/}}\n", gasPrice)
		})

		ginkgo.By("sending funds at the current gas price", func() {
			// Create a recipient address
			recipientKey, err := secp256k1.NewPrivateKey()
			require.NoError(err)
			recipientEthAddress := evm.GetEthAddress(recipientKey)

			// Create transaction
			nonce, err := ethClient.AcceptedNonceAt(e2e.DefaultContext(), ethAddress)
			require.NoError(err)
			tx := types.NewTx(&types.LegacyTx{
				Nonce:    nonce,
				GasPrice: gasPrice,
				Gas:      e2e.DefaultGasLimit,
				To:       &recipientEthAddress,
				Value:    common.Big0,
			})

			// Send the transaction and wait for acceptance
			signedTx := sign(tx)
			_ = e2e.SendEthTransaction(ethClient, signedTx)
		})

		e2e.CheckBootstrapIsPossible(privateNetwork)
	})
})
