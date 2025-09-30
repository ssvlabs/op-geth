package eth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/rpc"
)

// composeUserOpsAPI implements the `custom.compose_buildSignedUserOpsTx` RPC.
// It purposely lives in package `eth` to access sequencer signing facilities
// available on the concrete API backend.
type composeUserOpsAPI struct {
	b *EthAPIBackend
}

// Request JSON types
type userOperationV07 struct {
	Sender               common.Address `json:"sender"`
	Nonce                *hexutil.Big   `json:"nonce"`
	InitCode             hexutil.Bytes  `json:"initCode"`
	CallData             hexutil.Bytes  `json:"callData"`
	CallGasLimit         *hexutil.Big   `json:"callGasLimit"`
	VerificationGasLimit *hexutil.Big   `json:"verificationGasLimit"`
	PreVerificationGas   *hexutil.Big   `json:"preVerificationGas"`
	MaxFeePerGas         *hexutil.Big   `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *hexutil.Big   `json:"maxPriorityFeePerGas"`
	PaymasterAndData     hexutil.Bytes  `json:"paymasterAndData"`
	Signature            hexutil.Bytes  `json:"signature"`
}

type composeOpts struct {
	ChainID uint64 `json:"chainId"`
}

// Response JSON type
type SignedTxResp struct {
	Raw                  string   `json:"raw"`
	Hash                 string   `json:"hash"`
	To                   string   `json:"to"`
	ChainID              uint64   `json:"chainId"`
	Gas                  string   `json:"gas"`
	MaxFeePerGas         string   `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string   `json:"maxPriorityFeePerGas"`
	UserOpHashes         []string `json:"userOpHashes"`
}

// Minimal ABI JSON for EntryPoint v0.7 used here.
// Includes: balanceOf(address), getUserOpHash(PackedUserOperation),
//
//	handleOps(PackedUserOperation[],address)
const entryPointV07ABI = `[
  {"inputs":[{"internalType":"address","name":"account","type":"address"}],"name":"balanceOf","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
  {"inputs":[{"components":[
      {"internalType":"address","name":"sender","type":"address"},
      {"internalType":"uint256","name":"nonce","type":"uint256"},
      {"internalType":"bytes","name":"initCode","type":"bytes"},
      {"internalType":"bytes","name":"callData","type":"bytes"},
      {"internalType":"bytes32","name":"accountGasLimits","type":"bytes32"},
      {"internalType":"uint256","name":"preVerificationGas","type":"uint256"},
      {"internalType":"bytes32","name":"gasFees","type":"bytes32"},
      {"internalType":"bytes","name":"paymasterAndData","type":"bytes"},
      {"internalType":"bytes","name":"signature","type":"bytes"}
  ],"internalType":"struct PackedUserOperation","name":"userOp","type":"tuple"}],
   "name":"getUserOpHash","outputs":[{"internalType":"bytes32","name":"","type":"bytes32"}],"stateMutability":"view","type":"function"},
  {"inputs":[{"components":[
      {"internalType":"address","name":"sender","type":"address"},
      {"internalType":"uint256","name":"nonce","type":"uint256"},
      {"internalType":"bytes","name":"initCode","type":"bytes"},
      {"internalType":"bytes","name":"callData","type":"bytes"},
      {"internalType":"bytes32","name":"accountGasLimits","type":"bytes32"},
      {"internalType":"uint256","name":"preVerificationGas","type":"uint256"},
      {"internalType":"bytes32","name":"gasFees","type":"bytes32"},
      {"internalType":"bytes","name":"paymasterAndData","type":"bytes"},
      {"internalType":"bytes","name":"signature","type":"bytes"}
  ],"internalType":"struct PackedUserOperation[]","name":"ops","type":"tuple[]"},
  {"internalType":"address payable","name":"beneficiary","type":"address"}],
   "name":"handleOps","outputs":[],"stateMutability":"nonpayable","type":"function"}
]`

// Packed userop for ABI packing
type packedUserOp struct {
	Sender             common.Address `abi:"sender"`
	Nonce              *big.Int       `abi:"nonce"`
	InitCode           []byte         `abi:"initCode"`
	CallData           []byte         `abi:"callData"`
	AccountGasLimits   [32]byte       `abi:"accountGasLimits"`
	PreVerificationGas *big.Int       `abi:"preVerificationGas"`
	GasFees            [32]byte       `abi:"gasFees"`
	PaymasterAndData   []byte         `abi:"paymasterAndData"`
	Signature          []byte         `abi:"signature"`
}

// BuildSignedUserOpsTx is the RPC-exposed entry point.
// Final JSON-RPC method: compose_buildSignedUserOpsTx (namespace "compose").
func (api *composeUserOpsAPI) BuildSignedUserOpsTx(
	ctx context.Context,
	userOps []userOperationV07,
	opts composeOpts,
) (*SignedTxResp, error) {
	// Canonicalize & quick policy checks
	chainID := api.b.ChainConfig().ChainID.Uint64()
	if opts.ChainID == 0 || opts.ChainID != chainID {
		return nil, &rpc.JsonError{Code: -32001, Message: "wrongChainId", Data: map[string]any{"expected": chainID}}
	}

	// Always use the canonical v0.7 EntryPoint address.
	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	if len(userOps) == 0 {
		return nil, &rpc.JsonError{Code: -32602, Message: "invalidUserOperation", Data: map[string]any{"reason": "empty userOps"}}
	}
	if len(userOps) > 10 {
		return nil, &rpc.JsonError{Code: -32007, Message: "rateLimited", Data: map[string]any{"reason": "batch too large"}}
	}

	// For now, reject any paymaster usage to keep policy minimal & safe
	for i, op := range userOps {
		if len(op.PaymasterAndData) != 0 {
			return nil, &rpc.JsonError{Code: -32003, Message: "invalidUserOperation", Data: map[string]any{"opIndex": i, "reason": "paymasterAndData not supported"}}
		}
	}

	// Pull network fee context (for checks only; we no longer mutate user fee fields)
	tipSuggestion, err := api.b.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, err
	}
	head := api.b.CurrentHeader()
	baseFee := new(big.Int)
	if head != nil && head.BaseFee != nil {
		baseFee = new(big.Int).Set(head.BaseFee)
	}

	// ABI for EntryPoint
	parsedABI, err := abi.JSON(strings.NewReader(entryPointV07ABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse EntryPoint ABI: %w", err)
	}

	// Build packed ops & basic deposit checks
	packedOps := make([]packedUserOp, 0, len(userOps))
	userOpHashes := make([]string, 0, len(userOps))

	// Pre-encode helper to compute balanceOf & getUserOpHash via eth_call
	callAt := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)

	// Track min user fee caps across ops to set the outer tx caps without exceeding reimbursement
	minUserTip := (*big.Int)(nil)
	minUserFeeCap := (*big.Int)(nil)

	for i, op := range userOps {
		// Merge fee fields with server policy
		vgl := toBig(op.VerificationGasLimit)
		cgl := toBig(op.CallGasLimit)
		pvg := toBig(op.PreVerificationGas)
		if vgl.Sign() < 0 || cgl.Sign() < 0 || pvg.Sign() < 0 {
			return nil, &rpc.JsonError{Code: -32602, Message: "invalidUserOperation", Data: map[string]any{"opIndex": i, "reason": "negative gas not allowed"}}
		}

		// Pack gas pairs into bytes32 as per v0.7: (verificationGasLimit, callGasLimit)
		agl, ok := packPairToBytes32(vgl, cgl)
		if !ok {
			return nil, &rpc.JsonError{Code: -32005, Message: "gasCapExceeded", Data: map[string]any{"opIndex": i, "reason": "gas exceeds uint128 bounds"}}
		}

		// Use user-provided fees; do not mutate to preserve signature validity
		uTip := toBig(op.MaxPriorityFeePerGas)
		uFeeCap := toBig(op.MaxFeePerGas)
		gfees, ok := packPairToBytes32(uTip, uFeeCap)
		if !ok {
			return nil, &rpc.JsonError{Code: -32005, Message: "gasCapExceeded", Data: map[string]any{"opIndex": i, "reason": "fee exceeds uint128 bounds"}}
		}

		p := packedUserOp{
			Sender:             op.Sender,
			Nonce:              toBig(op.Nonce),
			InitCode:           op.InitCode,
			CallData:           op.CallData,
			AccountGasLimits:   agl,
			PreVerificationGas: pvg,
			GasFees:            gfees,
			PaymasterAndData:   nil, // enforced to empty above
			Signature:          op.Signature,
		}

		// Compute a conservative prefund bound; ensure deposit covers it
		// bound = (callGas + verificationGas + preVerificationGas) * uFeeCap
		gasSum := new(big.Int).Add(cgl, new(big.Int).Add(vgl, pvg))
		prefundBound := new(big.Int).Mul(gasSum, uFeeCap)

		// balanceOf(sender)
		data, err := parsedABI.Pack("balanceOf", op.Sender)
		if err != nil {
			return nil, fmt.Errorf("abi pack balanceOf: %w", err)
		}
		bal, err := api.callUint256(ctx, ep, data, callAt)
		if err != nil {
			return nil, fmt.Errorf("balanceOf call failed: %w", err)
		}
		if bal.Cmp(prefundBound) < 0 {
			return nil, &rpc.JsonError{Code: -32004, Message: "insufficientDeposit", Data: map[string]any{"opIndex": i, "required": prefundBound.String(), "deposit": bal.String()}}
		}

		// getUserOpHash(op)
		data, err = parsedABI.Pack("getUserOpHash", p)
		if err != nil {
			return nil, fmt.Errorf("abi pack getUserOpHash: %w", err)
		}
		hashBytes, err := api.callBytes32(ctx, ep, data, callAt)
		if err != nil {
			return nil, fmt.Errorf("getUserOpHash call failed: %w", err)
		}
		userOpHashes = append(userOpHashes, "0x"+hex.EncodeToString(hashBytes[:]))
		packedOps = append(packedOps, p)

		// Maintain minimum user fee caps across batch
		if minUserTip == nil || uTip.Cmp(minUserTip) < 0 {
			minUserTip = new(big.Int).Set(uTip)
		}
		if minUserFeeCap == nil || uFeeCap.Cmp(minUserFeeCap) < 0 {
			minUserFeeCap = new(big.Int).Set(uFeeCap)
		}
	}

	// Encode handleOps(ops, beneficiary)
	beneficiary := api.b.sequencerAddress // enforce reimbursement to sequencer
	callData, err := parsedABI.Pack("handleOps", packedOps, beneficiary)
	if err != nil {
		return nil, fmt.Errorf("abi pack handleOps: %w", err)
	}

	// Estimate gas for the call, add 15% safety margin
	from := api.b.sequencerAddress
	args := ethapi.TransactionArgs{
		From: &from,
		To:   &ep,
		Data: (*hexutil.Bytes)(&callData),
		// Fees are irrelevant for estimation
		Value: (*hexutil.Big)(big.NewInt(0)),
	}
	estGas, err := ethapi.DoEstimateGas(ctx, api.b, args, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber), nil, nil, api.b.RPCGasCap())
	if err != nil {
		// If estimation reverted, surface a simulateValidation-ish error
		return nil, &rpc.JsonError{Code: -32006, Message: "simulateValidationFailed", Data: map[string]any{"reason": err.Error()}}
	}
	gas := uint64(estGas)
	gas = gas + gas/6 // + ~16.6% safety

	// Decide outer tx fee caps so we don't overpay beyond reimbursement limits.
	if minUserTip == nil || minUserFeeCap == nil {
		return nil, &rpc.JsonError{Code: -32602, Message: "invalidUserOperation", Data: map[string]any{"reason": "no userOps"}}
	}
	// Quick sanity: ensure current inclusion is feasible
	// Require minUserFeeCap >= baseFee + minUserTip
	effNow := new(big.Int).Add(baseFee, minUserTip)
	if minUserFeeCap.Cmp(effNow) < 0 {
		return nil, &rpc.JsonError{Code: -32003, Message: "invalidUserOperation", Data: map[string]any{"reason": "user fee caps below current baseFee", "baseFee": baseFee.String(), "minUserTip": minUserTip.String(), "minUserFeeCap": minUserFeeCap.String(), "tipSuggestion": tipSuggestion.String()}}
	}

	// SSV: Calculate nonce for the user's handleOps transaction
	from = api.b.sequencerAddress
	poolNonce, err := api.b.GetPoolNonce(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("get pool nonce: %w", err)
	}

	// Use current pool nonce without reservation
	// putInbox transactions created during SBCP will be assigned nonces dynamically
	// to avoid conflicts with this transaction
	actualNonce := poolNonce

	// Compose and sign a type-2 tx from the sequencer EOA
	txData := &types.DynamicFeeTx{
		ChainID:   api.b.ChainConfig().ChainID,
		Nonce:     actualNonce,
		GasTipCap: new(big.Int).Set(minUserTip),
		GasFeeCap: new(big.Int).Set(minUserFeeCap),
		Gas:       gas,
		To:        &ep,
		Value:     big.NewInt(0),
		Data:      callData,
	}
	tx := types.NewTx(txData)
	signedTx, err := types.SignTx(tx, types.NewLondonSigner(api.b.ChainConfig().ChainID), api.b.sequencerKey)
	if err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}

	raw, err := signedTx.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal tx: %w", err)
	}

	// Build response
	resp := &SignedTxResp{
		Raw:                  "0x" + hex.EncodeToString(raw),
		Hash:                 signedTx.Hash().Hex(),
		To:                   ep.Hex(),
		ChainID:              chainID,
		Gas:                  hexutil.Uint64(gas).String(),
		MaxFeePerGas:         (*hexutil.Big)(minUserFeeCap).String(),
		MaxPriorityFeePerGas: (*hexutil.Big)(minUserTip).String(),
		UserOpHashes:         userOpHashes,
	}
	return resp, nil
}

// Backward-compatible aliases: keep older names mapping to the main method.
func (api *composeUserOpsAPI) ComposeBuildSignedUserOpsTx(ctx context.Context, userOps []userOperationV07, opts composeOpts) (*SignedTxResp, error) {
	return api.BuildSignedUserOpsTx(ctx, userOps, opts)
}
func (api *composeUserOpsAPI) Compose_buildSignedUserOpsTx(ctx context.Context, userOps []userOperationV07, opts composeOpts) (*SignedTxResp, error) {
	return api.BuildSignedUserOpsTx(ctx, userOps, opts)
}

// Helper: pack two uint128 values into a bytes32: (hi, lo)
func packPairToBytes32(hi, lo *big.Int) ([32]byte, bool) {
	var out [32]byte
	// Ensure both fit in 128 bits
	max128 := new(big.Int).Lsh(big.NewInt(1), 128)
	if hi.Sign() < 0 || lo.Sign() < 0 || hi.Cmp(max128) >= 0 || lo.Cmp(max128) >= 0 {
		return out, false
	}
	val := new(big.Int).Lsh(hi, 128)
	val.Add(val, lo)
	b := val.FillBytes(make([]byte, 32))
	copy(out[:], b)
	return out, true
}

func toBig(x *hexutil.Big) *big.Int {
	if x == nil {
		return new(big.Int)
	}
	return (*big.Int)(x)
}

// callUint256 calls a view method and returns the single uint256 output.
func (api *composeUserOpsAPI) callUint256(ctx context.Context, to common.Address, data []byte, at rpc.BlockNumberOrHash) (*big.Int, error) {
	from := api.b.sequencerAddress
	args := ethapi.TransactionArgs{From: &from, To: &to, Data: (*hexutil.Bytes)(&data)}
	res, err := ethapi.DoCall(ctx, api.b, args, at, nil, nil, api.b.RPCEVMTimeout(), api.b.RPCGasCap())
	if err != nil {
		return nil, err
	}
	// Return data should be 32 bytes
	if len(res.ReturnData) != 32 {
		return nil, errors.New("unexpected return length")
	}
	return new(big.Int).SetBytes(res.ReturnData), nil
}

// callBytes32 calls a view method and returns the single bytes32 output.
func (api *composeUserOpsAPI) callBytes32(ctx context.Context, to common.Address, data []byte, at rpc.BlockNumberOrHash) ([32]byte, error) {
	from := api.b.sequencerAddress
	args := ethapi.TransactionArgs{From: &from, To: &to, Data: (*hexutil.Bytes)(&data)}
	res, err := ethapi.DoCall(ctx, api.b, args, at, nil, nil, api.b.RPCEVMTimeout(), api.b.RPCGasCap())
	if err != nil {
		return [32]byte{}, err
	}
	if len(res.ReturnData) != 32 {
		return [32]byte{}, errors.New("unexpected return length")
	}
	var out [32]byte
	copy(out[:], res.ReturnData)
	return out, nil
}

// GetComposeUserOpsAPI returns the RPC API descriptor for registration.
func GetComposeUserOpsAPI(b *EthAPIBackend) rpc.API {
	return rpc.API{
		Namespace: "compose",
		Service:   &composeUserOpsAPI{b: b},
	}
}
