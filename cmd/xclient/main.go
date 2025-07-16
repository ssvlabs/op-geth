package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum/internal/xt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"google.golang.org/protobuf/proto"

	"github.com/ethereum/go-ethereum/internal/xt"
)

func main() {
	chainA_ID := big.NewInt(11111)
	chainB_ID := big.NewInt(22222)

	privateKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatal(err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		log.Fatal("cannot assert type: publicKey is not of type *ecdsa.PublicKey")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	tx1 := types.NewTransaction(
		0, // Nonce (for a real scenario, get this from ethclient)
		common.HexToAddress("0x71C7656EC7ab88b098defB751B7401B5f6d8976F"), // To
		big.NewInt(10000000000000000),                                     // 0.01 ETH
		21000,                                                             // Gas Limit
		big.NewInt(20000000000),                                           // Gas Price
		nil,                                                               // Data
	)
	signedTx1, err := types.SignTx(tx1, types.NewEIP155Signer(chainA_ID), privateKey)
	if err != nil {
		log.Fatal(err)
	}
	rlpSignedTx1, err := signedTx1.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}

	tx2 := types.NewTransaction(1, common.HexToAddress("0x..."), big.NewInt(2000), 21000, big.NewInt(20), nil)
	signedTx2, _ := types.SignTx(tx2, types.NewEIP155Signer(chainA_ID), privateKey)
	rlpSignedTx2, _ := signedTx2.MarshalBinary()

	tx3 := types.NewTransaction(0, common.HexToAddress("0x..."), big.NewInt(5000), 21000, big.NewInt(30), nil)
	signedTx3, _ := types.SignTx(tx3, types.NewEIP155Signer(chainB_ID), privateKey)
	rlpSignedTx3, _ := signedTx3.MarshalBinary()

	txRequestForChainA := &xt.TransactionRequest{
		ChainId: chainA_ID.Bytes(), // Convert big.Int to []byte
		Transaction: [][]byte{
			rlpSignedTx1,
			rlpSignedTx2,
		},
	}

	txRequestForChainB := &xt.TransactionRequest{
		ChainId: chainB_ID.Bytes(),
		Transaction: [][]byte{
			rlpSignedTx3,
		},
	}

	xtRequest := &xt.XTRequest{
		Transactions: []*xt.TransactionRequest{
			txRequestForChainA,
			txRequestForChainB,
		},
	}

	encodedPayload, err := proto.Marshal(xtRequest)
	if err != nil {
		log.Fatalf("Failed to marshal XTRequest: %v", err)
	}

	fmt.Printf("Successfully encoded payload. Size: %d bytes\n", len(encodedPayload))

	fmt.Println("\n--- Simulating RPC Call ---")

	client, err := rpc.Dial("http://localhost:8545")
	if err != nil {
		log.Fatalf("could not connect to custom rpc: %v", err)
	}
	defer client.Close()

	var resultHashes []common.Hash
	err = client.CallContext(context.Background(), &resultHashes, "txapi_sendRawXTransaction", encodedPayload)
	if err != nil {
		log.Fatalf("RPC call failed: %v", err)
	}

	fmt.Println("Successfully received hashes from custom RPC:")
	for i, hash := range resultHashes {
		fmt.Printf("  Tx %d Hash: %s\n", i+1, hash.Hex())
	}
}

func getDummyClient() *ethclient.Client {
	return nil
}
