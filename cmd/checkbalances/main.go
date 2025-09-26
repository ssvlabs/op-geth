package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"gopkg.in/yaml.v3"
)

const (
	configFile   = "config.yml"
	erc20Balance = `[{"name":"balanceOf","type":"function","stateMutability":"view","inputs":[{"name":"account","type":"address"}],"outputs":[{"name":"","type":"uint256"}]}]`
)

type Contracts struct {
	Token string `yaml:"token"`
}

type Rollup struct {
	RPC        string    `yaml:"rpc"`
	ChainID    int64     `yaml:"chain_id"`
	PrivateKey string    `yaml:"private_key"`
	Contracts  Contracts `yaml:"contracts"`
}

type Config struct {
	Token   string            `yaml:"token"`
	Rollups map[string]Rollup `yaml:"rollups"`
}

func main() {
	cfg := loadConfig(configFile)

	keys := []string{"A", "B"}
	for _, key := range keys {
		rollup, ok := cfg.Rollups[key]
		if !ok {
			log.Fatalf("rollup %q not found in config", key)
		}
		fmt.Printf("Rollup %s (%s)\n", key, rollup.RPC)

		privateKey := parsePrivateKey(rollup.PrivateKey)
		pubKey := privateKey.Public()
		pubKeyECDSA, _ := pubKey.(*ecdsa.PublicKey)
		address := crypto.PubkeyToAddress(*pubKeyECDSA)
		fmt.Printf("  Address: %s\n", address.Hex())

		client, err := ethclient.Dial(rollup.RPC)
		if err != nil {
			log.Fatalf("failed to connect to RPC %s: %v", rollup.RPC, err)
		}

		nativeBalance, err := client.BalanceAt(context.Background(), address, nil)
		if err != nil {
			log.Fatalf("failed to fetch native balance: %v", err)
		}
		fmt.Printf("  Native balance: %s wei\n", nativeBalance.String())

		tokenAddress := rollup.Contracts.Token
		if tokenAddress == "" {
			tokenAddress = cfg.Token
		}
		if tokenAddress != "" {
			balance, err := readERC20Balance(context.Background(), client, common.HexToAddress(tokenAddress), address)
			if err != nil {
				log.Fatalf("failed to fetch token balance: %v", err)
			}
			fmt.Printf("  Token balance (%s): %s\n", tokenAddress, balance.String())
		} else {
			fmt.Println("  Token balance: token address not configured")
		}

		fmt.Println()
		client.Close()
	}
}

func loadConfig(filename string) Config {
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Fatalf("failed to read config file %s: %v", filename, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("failed to parse YAML config: %v", err)
	}
	return cfg
}

func parsePrivateKey(hexKey string) *ecdsa.PrivateKey {
	if hexKey == "" {
		log.Fatal("private key cannot be empty")
	}
	if strings.HasPrefix(hexKey, "0x") {
		hexKey = hexKey[2:]
	}
	key, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		log.Fatalf("failed to parse private key: %v", err)
	}
	return key
}

func readERC20Balance(ctx context.Context, client *ethclient.Client, token, account common.Address) (*big.Int, error) {
	abiSpec, err := abi.JSON(strings.NewReader(erc20Balance))
	if err != nil {
		return nil, fmt.Errorf("parse erc20 abi: %w", err)
	}
	data, err := abiSpec.Pack("balanceOf", account)
	if err != nil {
		return nil, fmt.Errorf("pack balanceOf call: %w", err)
	}
	res, err := client.CallContract(ctx, ethereum.CallMsg{To: &token, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("call contract: %w", err)
	}
	return new(big.Int).SetBytes(res), nil
}
