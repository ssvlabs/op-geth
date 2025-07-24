package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"log"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/ethereum/go-ethereum/internal/xt"
)

const (
	sendTxRPCMethod = "eth_sendXTransaction"
	configFile      = "config.yml"
	mailboxAddr     = "0xEd3afBc0af3B010815dd242f1aA20d493Ae3160d"
)

type Rollup struct {
	RPC        string `yaml:"rpc"`
	ChainID    int64  `yaml:"chain_id"`
	PrivateKey string `yaml:"private_key"`
}

func (r *Rollup) GetChainID() *big.Int {
	return big.NewInt(r.ChainID)
}

type Config struct {
	Rollups map[string]Rollup `yaml:"rollups"`
}

func main() {
	config := loadConfigFromYAML(configFile)

	rollupA, exists := config.Rollups["A"]
	if !exists {
		log.Fatal("Rollup 'A' not found in configuration")
	}

	rollupB, exists := config.Rollups["B"]
	if !exists {
		log.Fatal("Rollup 'B' not found in configuration")
	}

	chainAId := rollupA.GetChainID()
	chainBId := rollupB.GetChainID()

	privateKeyA := parsePrivateKey(rollupA.PrivateKey)
	privateKeyB := parsePrivateKey(rollupB.PrivateKey)

	publicKey := privateKeyA.Public()
	publicKeyECDSA, _ := publicKey.(*ecdsa.PublicKey)
	addressA := crypto.PubkeyToAddress(*publicKeyECDSA)

	publicKey = privateKeyB.Public()
	publicKeyECDSA, _ = publicKey.(*ecdsa.PublicKey)
	addressB := crypto.PubkeyToAddress(*publicKeyECDSA)

	nonceA, err := getNonceFor(rollupA.RPC, addressA)
	if err != nil {
		log.Fatal(err)
	}

	nonceB, err := getNonceFor(rollupB.RPC, addressB)
	if err != nil {
		log.Fatal(err)
	}

	// Create ping-pong parameters
	sessionId := big.NewInt(12345)
	pingData := []byte("hello from rollup A")
	pongData := []byte("hello from rollup B")
	label := []byte("test-session")

	// Create a ping transaction (A -> B)
	pingParams := PingPongParams{
		ChainSrc:  chainAId,
		ChainDest: chainBId,
		Sender:    addressA,
		Receiver:  addressB,
		SessionId: sessionId,
		Data:      pingData,
		Label:     label,
	}

	signedTx1, err := createPingTransaction(pingParams, nonceA, privateKeyA)
	if err != nil {
		log.Fatal("Failed to create ping transaction:", err)
	}

	rlpSignedTx1, err := signedTx1.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}

	// Create a pong transaction (B -> A)
	pongParams := PingPongParams{
		ChainSrc:  chainBId,
		ChainDest: chainAId,
		Sender:    addressB,
		Receiver:  addressA,
		SessionId: sessionId,
		Data:      pongData,
		Label:     label,
	}

	signedTx2, err := createPongTransaction(pongParams, nonceB, privateKeyB)
	if err != nil {
		log.Fatal("Failed to create pong transaction:", err)
	}

	rlpSignedTx2, err := signedTx2.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}

	xtRequest := &xt.XTRequest{
		Transactions: []*xt.TransactionRequest{
			{
				ChainId: chainAId.Bytes(),
				Transaction: [][]byte{
					rlpSignedTx1,
				},
			},
			{
				ChainId: chainBId.Bytes(),
				Transaction: [][]byte{
					rlpSignedTx2,
				},
			},
		},
	}

	spMsg := &xt.Message{
		SenderId: "localhost1",
		Payload: &xt.Message_XtRequest{
			XtRequest: xtRequest,
		},
	}

	encodedPayload, err := proto.Marshal(spMsg)
	if err != nil {
		log.Fatalf("Failed to marshal XTRequest: %v", err)
	}

	fmt.Printf("Successfully encoded ping-pong payload. Size: %d bytes\n", len(encodedPayload))
	fmt.Printf("Session ID: %d\n", sessionId.Int64())
	fmt.Printf("Ping data: %s\n", string(pingData))
	fmt.Printf("Pong data: %s\n", string(pongData))

	l1Client, err := rpc.Dial(rollupA.RPC)
	if err != nil {
		log.Fatalf("could not connect to custom rpc: %v", err)
	}
	defer l1Client.Close()

	var resultHashes []common.Hash
	err = l1Client.CallContext(context.Background(), &resultHashes, sendTxRPCMethod, hexutil.Encode(encodedPayload))
	if err != nil {
		log.Fatalf("RPC call failed: %v", err)
	}

	fmt.Println("Successfully received hashes from custom RPC:")
	for i, hash := range resultHashes {
		fmt.Printf("  Tx %d Hash: %s\n", i+1, hash.Hex())
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

	if len(privKeyHex) >= 2 && privKeyHex[:2] == "0x" {
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

	nonce, err := client.PendingNonceAt(context.Background(), address)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve pending nonce: %w", err)
	}

	return nonce, nil
}
