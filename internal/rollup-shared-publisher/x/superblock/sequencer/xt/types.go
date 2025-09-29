package xt

import "github.com/ethereum/go-ethereum/common"

// ChainTxHash pairs a chain identifier with the resulting transaction hash. The
// JSON tags mirror the structure served over RPC responses.
type ChainTxHash struct {
	ChainID string      `json:"chainId"`
	Hash    common.Hash `json:"hash"`
}

// XTResult carries the outcome of processing an XT request. Hashes is non-empty
// on success; Err is non-nil if processing failed or the XT was aborted.
type XTResult struct {
	Hashes []ChainTxHash
	Err    error
}