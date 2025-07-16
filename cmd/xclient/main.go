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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"google.golang.org/protobuf/proto"

	"github.com/ethereum/go-ethereum/internal/xt"
)

const (
	L2_A_ADDRESS = "http://localhost:8545"
	L2_B_ADDRESS = "http://localhost:10545"
)

func main() {
	chainA_ID := big.NewInt(11111)
	chainB_ID := big.NewInt(22222)

	privateKeyA, privateKeyB := getPrivateKeysFromEnv()

	publicKey := privateKeyA.Public()
	publicKeyECDSA, _ := publicKey.(*ecdsa.PublicKey)
	addressA := crypto.PubkeyToAddress(*publicKeyECDSA)

	publicKey = privateKeyB.Public()
	publicKeyECDSA, _ = publicKey.(*ecdsa.PublicKey)
	//addressB := crypto.PubkeyToAddress(*publicKeyECDSA)

	nonceA, err := getNonceFor(L2_A_ADDRESS, addressA)
	if err != nil {
		log.Fatal(err)
	}

	nonceB := uint64(0)
	//nonceB, err := getNonceFor(L2_B_ADDRESS, addressB)
	//if err != nil {
	//	log.Fatal(err)
	//}

	toAddressA := common.HexToAddress("0x71C7656EC7ab88b098defB751B7401B5f6d8976F")
	txDataA := &types.DynamicFeeTx{
		ChainID:    chainA_ID,
		Nonce:      nonceA,
		GasTipCap:  big.NewInt(1000000000),
		GasFeeCap:  big.NewInt(20000000000),
		Gas:        21000,
		To:         &toAddressA,
		Value:      big.NewInt(1000000000000000),
		Data:       nil,
		AccessList: nil,
	}

	tx1 := types.NewTx(txDataA)

	signedTx1, err := types.SignTx(tx1, types.NewLondonSigner(chainA_ID), privateKeyA)
	if err != nil {
		log.Fatal(err)
	}
	rlpSignedTx1, err := signedTx1.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}

	toAddressB := common.HexToAddress("0x0000000000000000000000000000000000000001")
	txDataB := &types.DynamicFeeTx{
		ChainID:    chainB_ID,
		Nonce:      nonceB,
		GasTipCap:  big.NewInt(1000000000),
		GasFeeCap:  big.NewInt(20000000000),
		Gas:        21000,
		To:         &toAddressB,
		Value:      big.NewInt(1000000000000000),
		Data:       nil,
		AccessList: nil,
	}

	tx2 := types.NewTx(txDataB)

	signedTx2, err := types.SignTx(tx2, types.NewLondonSigner(chainB_ID), privateKeyB)
	if err != nil {
		log.Fatal(err)
	}
	rlpSignedTx2, err := signedTx2.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}

	xtRequest := &xt.XTRequest{
		Transactions: []*xt.TransactionRequest{
			{
				ChainId: chainA_ID.Bytes(),
				Transaction: [][]byte{
					rlpSignedTx1,
				},
			},
			{
				ChainId: chainB_ID.Bytes(),
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

	fmt.Printf("Successfully encoded payload. Size: %d bytes\n", len(encodedPayload))

	l1Client, err := rpc.Dial("http://localhost:8545")
	if err != nil {
		log.Fatalf("could not connect to custom rpc: %v", err)
	}
	defer l1Client.Close()

	var resultHashes []common.Hash
	err = l1Client.CallContext(context.Background(), &resultHashes, "eth_sendXTransaction", hexutil.Encode(encodedPayload))
	if err != nil {
		log.Fatalf("RPC call failed: %v", err)
	}

	fmt.Println("Successfully received hashes from custom RPC:")
	for i, hash := range resultHashes {
		fmt.Printf("  Tx %d Hash: %s\n", i+1, hash.Hex())
	}
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

func getPrivateKeysFromEnv() (*ecdsa.PrivateKey, *ecdsa.PrivateKey) {
	privKeyHexA := os.Getenv("PRIV_KEY_A")
	privKeyHexB := os.Getenv("PRIV_KEY_B")

	if privKeyHexA == "" {
		log.Fatal("PRIV_KEY_A environment variable not set")
	}
	if privKeyHexB == "" {
		log.Fatal("PRIV_KEY_B environment variable not set")
	}

	if len(privKeyHexA) >= 2 && privKeyHexA[:2] == "0x" {
		privKeyHexA = privKeyHexA[2:]
	}
	if len(privKeyHexB) >= 2 && privKeyHexB[:2] == "0x" {
		privKeyHexB = privKeyHexB[2:]
	}

	privateKeyA, err := crypto.HexToECDSA(privKeyHexA)
	if err != nil {
		log.Fatalf("Failed to parse PRIV_KEY_A: %v", err)
	}

	privateKeyB, err := crypto.HexToECDSA(privKeyHexB)
	if err != nil {
		log.Fatalf("Failed to parse PRIV_KEY_B: %v", err)
	}

	return privateKeyA, privateKeyB
}
