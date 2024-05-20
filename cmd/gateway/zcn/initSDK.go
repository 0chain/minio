package zcn

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/0chain/gosdk/core/conf"
	"github.com/0chain/gosdk/core/logger"
	"github.com/0chain/gosdk/zboxcore/blockchain"
	"github.com/0chain/gosdk/zboxcore/sdk"
	"github.com/0chain/gosdk/zcncore"
	"github.com/mitchellh/go-homedir"
)

type serverOptions struct {
	Encrypt  bool `json:"encrypt"`
	Compress bool `json:"compress"`
}

func initializeSDK(configDir, allocid string, nonce int64) error {
	if configDir == "" {
		var err error
		configDir, err = getDefaultConfigDir()
		if err != nil {
			return err
		}
	}

	if _, err := os.Stat(configDir); err != nil {
		return err
	}

	if allocid == "" {
		allocFile := filepath.Join(configDir, "allocation.txt")
		allocBytes, err := os.ReadFile(allocFile)
		if err != nil {
			return err
		}

		allocationID = strings.ReplaceAll(string(allocBytes), " ", "")
		allocationID = strings.ReplaceAll(allocationID, "\n", "")

		if len(allocationID) != 64 {
			return fmt.Errorf("allocation id has length %d, should be 64", len(allocationID))
		}
	}

	optionFile := filepath.Join(configDir, "zs3server.json")
	optionBytes, err := os.ReadFile(optionFile)
	if err == nil {
		var options serverOptions
		err = json.Unmarshal(optionBytes, &options)
		if err != nil {
			return err
		}
		encrypt = options.Encrypt
		compress = options.Compress
	}

	cfg, err := conf.LoadConfigFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		return err
	}

	walletFile := filepath.Join(configDir, "wallet.json")

	walletBytes, err := ioutil.ReadFile(walletFile)
	if err != nil {
		return err
	}

	network, _ := conf.LoadNetworkFile(filepath.Join(configDir, "network.yaml"))
	if network.IsValid() {
		zcncore.SetNetwork(network.Miners, network.Sharders)
		conf.InitChainNetwork(&conf.Network{
			Miners:   network.Miners,
			Sharders: network.Sharders,
		})
	}

	logger.SyncLoggers([]*logger.Logger{zcncore.GetLogger(), sdk.GetLogger()})
	zcncore.SetLogFile("cmdlog.log", true)
	sdk.SetLogFile("cmd.log", true)
	sdk.SetLogLevel(2)
	zcncore.SetLogLevel(2)

	err = zcncore.InitZCNSDK(cfg.BlockWorker, cfg.SignatureScheme,
		zcncore.WithChainID(cfg.ChainID),
		zcncore.WithMinSubmit(cfg.MinSubmit),
		zcncore.WithMinConfirmation(cfg.MinConfirmation),
		zcncore.WithConfirmationChainLength(cfg.ConfirmationChainLength))
	if err != nil {
		return err
	}

	err = sdk.InitStorageSDK(string(walletBytes), cfg.BlockWorker, cfg.ChainID, cfg.SignatureScheme, cfg.PreferredBlobbers, nonce)
	if err != nil {
		return err
	}

	blockchain.SetMaxTxnQuery(cfg.MaxTxnQuery)
	blockchain.SetQuerySleepTime(cfg.QuerySleepTime)
	conf.InitClientConfig(&cfg)

	if network.IsValid() {
		sdk.SetNetwork(network.Miners, network.Sharders)
	}

	sdk.SetNumBlockDownloads(10)
	return nil
}

func getDefaultConfigDir() (string, error) {
	homeDir, err := homedir.Dir()
	if err != nil {
		return "", err
	}

	configDir := filepath.Join(homeDir, ".zcn")

	return configDir, nil
}
