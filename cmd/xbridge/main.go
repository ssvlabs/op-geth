package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	rollupv1 "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

const (
	sendTxRPCMethod = "eth_sendXTransaction"
	configFile      = "config.yml"
)

type Rollup struct {
	RPC        string `yaml:"rpc"`
	ChainID    int64  `yaml:"chain_id"`
	PrivateKey string `yaml:"private_key"`
	Bridge     string `yaml:"bridge"`
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

	log.Printf("using address: %v", addressA)

	tokenA := common.HexToAddress(config.Token)
	bridgeA := common.HexToAddress(rollupA.Bridge)
	bridgeB := common.HexToAddress(rollupB.Bridge)

	// Create bridge parameters
	sessionId := generateRandomSessionID()
	amount := big.NewInt(100)

	// Create a send transaction (A -> B)
	sendParams := BridgeParams{
		ChainSrc:   chainAId, // 11111
		ChainDest:  chainBId, // 22222
		Token:      tokenA,
		Sender:     addressA,
		Receiver:   addressB,
		Amount:     amount,
		SessionId:  sessionId,
		DestBridge: bridgeB,
		SrcBridge:  bridgeA,
	}

	fmt.Println(sendParams)

	nonceA, err := getNonceFor(rollupA.RPC, addressA)
	if err != nil {
		log.Fatal(err)
	}

	signedTx1, err := createSendTransaction(sendParams, nonceA, privateKeyA, bridgeA)
	if err != nil {
		log.Fatal("Failed to create send transaction:", err)
	}

	rlpSignedTx1, err := signedTx1.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}

	// Create a receive transaction (B -> A)
	receiveParams := BridgeParams{
		ChainSrc:   chainAId, // 11111
		ChainDest:  chainBId, // 22222
		Token:      tokenA,
		Sender:     addressA,
		Receiver:   addressB,
		Amount:     amount,
		SessionId:  sessionId,
		DestBridge: bridgeB,
		SrcBridge:  bridgeA,
	}

	nonceB, err := getNonceFor(rollupB.RPC, addressB)
	if err != nil {
		log.Fatal(err)
	}

	signedTx2, err := createReceiveTransaction(receiveParams, nonceB, privateKeyB, bridgeB)
	if err != nil {
		log.Fatal("Failed to create receive transaction:", err)
	}

	rlpSignedTx2, err := signedTx2.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}

	xtRequest := &rollupv1.XTRequest{
		Transactions: []*rollupv1.TransactionRequest{
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

	spMsg := &rollupv1.Message{
		SenderId: "client",
		Payload: &rollupv1.Message_XtRequest{
			XtRequest: xtRequest,
		},
	}

	encodedPayload, err := proto.Marshal(spMsg)
	if err != nil {
		log.Fatalf("Failed to marshal XTRequest: %v", err)
	}

	fmt.Printf("Successfully encoded send-receive payload. Size: %d bytes\n", len(encodedPayload))
	fmt.Printf("Session ID: %d\n", sessionId.Int64())
	fmt.Printf("Send amount: %d\n", amount.Int64())
	fmt.Printf("Receive amount: %d\n", amount.Int64())

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

	fmt.Printf("Submitted %d transactions: %v\n", len(resultHashes), resultHashes)
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

// generateRandomSessionID returns a random big.Int in the range [0, 2^63-1]
func generateRandomSessionID() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 63)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		log.Fatalf("failed to generate random session ID: %v", err)
	}
	return n
}
