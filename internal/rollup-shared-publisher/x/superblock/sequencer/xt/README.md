# XT Result Tracker

The `xt` package provides result tracking for Cross-Transaction (XT) requests in the Rollup Shared Publisher system.

## Overview

When cross-chain transactions are submitted via RPC, the sequencer needs to track their execution results across multiple rollup chains. This package implements a pub/sub pattern that allows RPC handlers to wait for transaction execution results without blocking the main consensus flow.

## Components

### Types (`types.go`)

- **`ChainTxHash`**: Pairs a chain identifier with a transaction hash, representing where a cross-chain transaction was executed
- **`XTResult`**: Carries the outcome of processing an XT request, containing either successful transaction hashes or an error

### Result Tracker (`tracker.go`)

- **`XTResultTracker`**: Thread-safe coordinator for one-shot result subscriptions
  - `Subscribe(xtID)`: Register a waiter for a specific XT ID and receive a result channel
  - `Publish(xtID, hashes)`: Deliver successful execution results to subscribers
  - `PublishError(xtID, err)`: Notify subscribers of execution failures

## Usage

### Subscribing to Results

```go
tracker := xt.NewXTResultTracker()

// Subscribe to wait for results
resultCh, cancel, err := tracker.Subscribe(xtID)
if err != nil {
    return err
}
defer cancel()

// Wait for result with timeout
select {
case result := <-resultCh:
    if result.Err != nil {
        return result.Err
    }
    return result.Hashes
case <-time.After(30 * time.Second):
    return errors.New("timeout waiting for XT result")
}
```

### Publishing Results

```go
// On successful execution
hashes := []xt.ChainTxHash{
    {ChainID: "1", Hash: txHash1},
    {ChainID: "10", Hash: txHash2},
}
tracker.Publish(xtID, hashes)

// On failure
tracker.PublishError(xtID, fmt.Errorf("execution failed"))
```

## Design Considerations

- **One-shot delivery**: Each subscription receives exactly one result (success or error)
- **Thread-safe**: All operations are protected by internal locking
- **No duplicate subscriptions**: Attempting to subscribe twice to the same XT ID returns an error
- **Automatic cleanup**: Results are delivered once and channels are closed immediately
- **Deduplication**: The tracker clones result slices to prevent external mutation

## Integration Points

This package is used by:

- **RPC API** (`internal/ethapi/api.go`): `SendXTransaction` subscribes to track submitted transactions
- **Backend** (`eth/api_backend.go`): `simulateXTRequestForSBCP` publishes results after consensus completes
- **Sequencer coordination**: Results are published after the two-phase commit protocol completes

## Thread Safety

All public methods are thread-safe and can be called concurrently from multiple goroutines. The tracker uses a single mutex to protect its internal waiter map.