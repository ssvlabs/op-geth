package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"gopkg.in/yaml.v3"
)

const (
	sendTxRPCMethod = "eth_sendRawTransaction"
	configFile      = "config.yml"
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

func (r *Rollup) GetChainID() *big.Int {
	return big.NewInt(r.ChainID)
}

type Config struct {
	Token   string            `yaml:"token"`
	Rollups map[string]Rollup `yaml:"rollups"`
}

func main() {
	config := loadConfigFromYAML(configFile)

	keys := []string{"A", "B"}
	amount := big.NewInt(0).Exp(big.NewInt(10), big.NewInt(18), nil) // 1 token assuming 18 decimals

	for _, key := range keys {
		rollup, exists := config.Rollups[key]
		if !exists {
			log.Fatalf("Rollup %q not found in configuration", key)
		}

		tokenAddress := rollup.Contracts.Token
		if tokenAddress == "" {
			tokenAddress = config.Token
		}
		if tokenAddress == "" {
			log.Fatalf("Token address missing for rollup %s", key)
		}

		chainID := rollup.GetChainID()
		privateKey := parsePrivateKey(rollup.PrivateKey)
		publicKey := privateKey.Public()
		publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
		if !ok {
			log.Fatalf("unexpected public key type for rollup %s", key)
		}
		address := crypto.PubkeyToAddress(*publicKeyECDSA)

		mintParams := MintParams{
			ChainSrc: chainID,
			Receiver: address,
			Amount:   new(big.Int).Set(amount),
			Token:    common.HexToAddress(tokenAddress),
		}

		nonce, err := getNonceFor(rollup.RPC, address)
		if err != nil {
			log.Fatalf("failed to fetch nonce for rollup %s: %v", key, err)
		}

		signedTx, err := createMintTransaction(mintParams, nonce, privateKey)
		if err != nil {
			log.Fatalf("failed to create mint transaction for rollup %s: %v", key, err)
		}

		rlpSignedTx, err := signedTx.MarshalBinary()
		if err != nil {
			log.Fatalf("failed to encode transaction for rollup %s: %v", key, err)
		}

		client, err := rpc.Dial(rollup.RPC)
		if err != nil {
			log.Fatalf("could not connect to RPC %s: %v", rollup.RPC, err)
		}
		var txHash common.Hash
		raw := hexutil.Encode(rlpSignedTx)
		err = client.CallContext(context.Background(), &txHash, sendTxRPCMethod, raw)
		client.Close()
		if err != nil {
			log.Fatalf("RPC call failed on rollup %s: %v", key, err)
		}

		fmt.Printf("Minted %s wei on rollup %s. tx hash: %s\n", amount.String(), key, txHash.Hex())
	}
}

func loadConfigFromYAML(filename string) Config {
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Fatalf("Failed to read config file %s: %v", filename, err)
	}

	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		log.Fatalf("Failed to parse YAML config: %v", err)
	}

	return config
}

func parsePrivateKey(privKeyHex string) *ecdsa.PrivateKey {
	if privKeyHex == "" {
		log.Fatal("Private key cannot be empty")
	}

	if strings.HasPrefix(privKeyHex, "0x") {
		privKeyHex = privKeyHex[2:]
	}

	privateKey, err := crypto.HexToECDSA(privKeyHex)
	if err != nil {
		log.Fatalf("Failed to parse private key: %v", err)
	}

	return privateKey
}

func getNonceFor(networkRPCAddr string, address common.Address) (uint64, error) {
	client, err := ethclient.Dial(networkRPCAddr)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	nonce, err := client.PendingNonceAt(context.Background(), address)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve pending nonce: %w", err)
	}

	return nonce, nil
}
