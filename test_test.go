package dymension_interchaintest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/math"
	simappparams "github.com/cosmos/cosmos-sdk/simapp/params"
	sdk "github.com/cosmos/cosmos-sdk/types"
	transfertypes "github.com/cosmos/ibc-go/v6/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v6/modules/core/02-client/types"
	"github.com/strangelove-ventures/interchaintest/v6"
	"github.com/strangelove-ventures/interchaintest/v6/chain/cosmos"
	"github.com/strangelove-ventures/interchaintest/v6/ibc"
	interchaintestrelayer "github.com/strangelove-ventures/interchaintest/v6/relayer"
	"github.com/strangelove-ventures/interchaintest/v6/testreporter"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	ethermintcrypto "github.com/evmos/ethermint/crypto/codec"
	ethermint "github.com/evmos/ethermint/types"
)

func evmConfig() *simappparams.EncodingConfig {
	cfg := cosmos.DefaultEncoding()

	ethermint.RegisterInterfaces(cfg.InterfaceRegistry)
	ethermintcrypto.RegisterInterfaces(cfg.InterfaceRegistry)

	return &cfg
}

func TestDymensionIBC(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ctx := context.Background()
	numFullNodes := 0
	numValidators := 1
	t.Parallel()
	stakingAmount, _ := math.NewIntFromString("500000000000000000000000")
	genesisbalance, _ := math.NewIntFromString("1000000000000000000000000")
	cf := interchaintest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*interchaintest.ChainSpec{
		{Name: "dymension", Version: "latest", NumFullNodes: &numFullNodes, NumValidators: &numValidators,
			ChainConfig: ibc.ChainConfig{
				Type:           "cosmos",
				ChainID:        "dymension_100-1",
				Images:         []ibc.DockerImage{{Repository: "dymension", UidGid: "1025:1025"}},
				Bin:            "dymd",
				Bech32Prefix:   "dym",
				Denom:          "udym",
				GasPrices:      "0udym",
				EncodingConfig: evmConfig(),
				GasAdjustment:  1.5,
				TrustingPeriod: "168h0m0s",
				ModifyGenesisAmounts: func() (sdk.Coin, sdk.Coin) {
					return sdk.NewCoin("udym", genesisbalance),
						sdk.NewCoin("udym", stakingAmount)
				},
			}},
		{Name: "osmosis", Version: "v11.0.0", NumFullNodes: &numFullNodes, NumValidators: &numValidators},
	})
	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)
	dymension, osmosis := chains[0], chains[1]
	client, network := interchaintest.DockerSetup(t)

	r := interchaintest.NewBuiltinRelayerFactory(ibc.CosmosRly, zaptest.NewLogger(t),
		// https://github.com/cosmos/relayer/commit/d3c1724def2a174e417139108ff2d6f5d0c81412
		interchaintestrelayer.CustomDockerImage("ghcr.io/cosmos/relayer", "reece-v2.3.1-ethermint", "100:1000"),
		interchaintestrelayer.StartupFlags("--processor", "events", "--block-history", "100"),
	).Build(t, client, network)

	const ibcPath = "dymension-osmo-demo"
	ic := interchaintest.NewInterchain().
		AddChain(dymension).
		AddChain(osmosis).
		AddRelayer(r, "relayer").
		AddLink(interchaintest.InterchainLink{
			Chain1:  dymension,
			Chain2:  osmosis,
			Relayer: r,
			Path:    ibcPath,
		})

	// Log location
	f, err := interchaintest.CreateLogFile(fmt.Sprintf("%d.json", time.Now().Unix()))
	require.NoError(t, err)
	// Reporter/logs
	rep := testreporter.NewReporter(f)
	eRep := rep.RelayerExecReporter(t)

	// Build interchain
	require.NoError(t, ic.Build(ctx, eRep, interchaintest.InterchainBuildOptions{
		TestName:  t.Name(),
		Client:    client,
		NetworkID: network,
		// BlockDatabaseFile: interchaintest.DefaultBlockDatabaseFilepath(),
		SkipPathCreation: false},
	),
	)

	// Create and Fund User Wallets
	fundAmount := math.NewInt(10_000_000)
	users := interchaintest.GetAndFundTestUsers(t, ctx, "default", fundAmount, dymension, osmosis)
	gaiaUser := users[0]
	osmosisUser := users[1]

	gaiaUserBalInitial, err := dymension.GetBalance(ctx, gaiaUser.FormattedAddress(), dymension.Config().Denom)
	require.NoError(t, err)
	require.True(t, gaiaUserBalInitial.Equal(fundAmount))

	// Get Channel ID
	gaiaChannelInfo, err := r.GetChannels(ctx, eRep, dymension.Config().ChainID)
	require.NoError(t, err)
	gaiaChannelID := gaiaChannelInfo[0].ChannelID

	osmoChannelInfo, err := r.GetChannels(ctx, eRep, osmosis.Config().ChainID)
	require.NoError(t, err)
	osmoChannelID := osmoChannelInfo[0].ChannelID

	height, err := osmosis.Height(ctx)
	require.NoError(t, err)

	// Send Transaction
	amountToSend := math.NewInt(1_000_000)
	dstAddress := osmosisUser.FormattedAddress()
	transfer := ibc.WalletAmount{
		Address: dstAddress,
		Denom:   dymension.Config().Denom,
		Amount:  amountToSend,
	}
	tx, err := dymension.SendIBCTransfer(ctx, gaiaChannelID, gaiaUser.KeyName(), transfer, ibc.TransferOptions{})
	require.NoError(t, err)
	require.NoError(t, tx.Validate())

	// relay MsgRecvPacket to osmosis, then MsgAcknowledgement back to gaia
	require.NoError(t, r.Flush(ctx, eRep, ibcPath, gaiaChannelID))

	// test source wallet has decreased funds
	expectedBal := gaiaUserBalInitial.Sub(amountToSend)
	gaiaUserBalNew, err := dymension.GetBalance(ctx, gaiaUser.FormattedAddress(), dymension.Config().Denom)
	require.NoError(t, err)
	require.True(t, gaiaUserBalNew.Equal(expectedBal))

	// Trace IBC Denom
	srcDenomTrace := transfertypes.ParseDenomTrace(transfertypes.GetPrefixedDenom("transfer", osmoChannelID, dymension.Config().Denom))
	dstIbcDenom := srcDenomTrace.IBCDenom()

	// Test destination wallet has increased funds
	osmosUserBalNew, err := osmosis.GetBalance(ctx, osmosisUser.FormattedAddress(), dstIbcDenom)
	require.NoError(t, err)
	require.True(t, osmosUserBalNew.Equal(amountToSend))

	// Validate light client
	chain := osmosis.(*cosmos.CosmosChain)
	reg := chain.Config().EncodingConfig.InterfaceRegistry
	msg, err := cosmos.PollForMessage[*clienttypes.MsgUpdateClient](ctx, chain, reg, height, height+10, nil)
	require.NoError(t, err)

	require.Equal(t, "07-tendermint-0", msg.ClientId)
	require.NotEmpty(t, msg.Signer)
}
