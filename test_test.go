package dymension_interchaintest

import (
	"github.com/strangelove-ventures/interchaintest/v7"
	"github.com/strangelove-ventures/interchaintest/v7/ibc"
	"go.uber.org/zap/zaptest"
	"testing"
)

func TestLearn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	t.Parallel()

	cf := interchaintest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*interchaintest.ChainSpec{
		{Name: "gaia", Version: "v7.0.0", ChainConfig: ibc.ChainConfig{
			GasPrices: "0.0uatom",
		}},
		{Name: "osmosis", Version: "v11.0.0"},
	})
	_, err := cf.Chains(t.Name())
	if err != nil {

	}
}
