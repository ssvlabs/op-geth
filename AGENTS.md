# Compose Rollup Integration in op-geth

This fork of go-ethereum stitches the OP Stack execution client into SSV Labs' "Compose" environment. Two rollups share a
common Shared Publisher (SP) that orchestrates synchronous cross-rollup transactions ("xTs") and slot-based block
building. The notes below trace the moving pieces you will touch most often when iterating on synchronous composability.

## Control Plane Overview
- **Shared Publisher transport**: At boot (`node/node.go`), we call `bootstrap.Setup` to wire a sequencer coordinator,
  TCP SP client, peer-to-peer CIRC transport, and per-rollup peer clients. The runtime handles authentication (optional),
  retries, and slot timers and exposes them back through the node so downstream services can reference
  `stack.SPClient()`, `stack.Coordinator()`, and `stack.SequencerClients()`.
- **EthAPI backend wiring**: `eth/backend.go` injects those handles into `EthAPIBackend` and immediately calls
  `SetSequencerCoordinator`. That method registers inbound message handlers, consensus callbacks, and miner hooks so the
  execution layer can react to SP instructions (StartSlot, RequestSeal, StartSC) and peer-to-peer CIRC gossip.

## RPC Surface – `eth_sendXTransaction`
- The new RPC entry point lives in `internal/ethapi/api.go` as `TransactionAPI.SendXTransaction`. Clients submit a
  hex-encoded `rollup.v1.Message` carrying an `XTRequest` bundle. We unmarshal the proto, tag the RPC context with
  `forward=true`, and hand the message to `EthAPIBackend.HandleSPMessage`.
- `HandleSPMessage` short-circuits the normal coordinator path when `forward` is set: the payload is forwarded directly
  to the SP over the TCP client so the leader can start 2PC. Any other inbound message (votes, decisions, StartSlot, etc.)
  is fed into the local sequencer coordinator for processing.

## Sequencer Coordinator & Consensus Hooks
- `EthAPIBackend.SetSequencerCoordinator` binds the SBCP coordinator to the SP client and peer transports. It also wires
  consensus callbacks for vote forwarding, CIRC buffering, and block lifecycle notifications. Each peer client gets a
  handler that routes inbound CIRC messages back into `handleSequencerMessage`, keeping the execution node in sync with
  remote sequencers.
- The backend exposes helper callbacks (`StartCallbackFn`, `VoteCallbackFn`, `CIRCCallbackFn`) that the consensus layer
  uses to issue votes, queue re-simulations when CIRC dependencies arrive, and (in SBCP mode) silence legacy start
  callbacks.

## Mailbox-Driven Synchronous Composability
- The heavy lifting happens in `simulateXTRequestForSBCP` and `MailboxProcessor` (`eth/api_backend.go` and
  `eth/ssv_mailbox_processor.go`). For every local transaction inside an xT request we:
  1. Simulate the payload with the native SSV tracer to capture mailbox reads/writes.
  2. Classify mailbox access—reads become dependencies, writes become outbound messages.
  3. Dispatch outbound messages as CIRC payloads to remote sequencers via the peer clients.
  4. Block on dependencies by draining the consensus-layer CIRC queue for matching ACKs, then stage `putInbox` writes.
  5. Re-simulate after `putInbox` to detect ACK writes that must be echoed back out, ensuring both sides observe the
     same mailbox state.
- Staged `putInbox` and `clear()` calls are signed with the sequencer key and fed through `SubmitSequencerTransaction`
  so they land in the local txpool and miner pending set. Completed transactions are pooled only once their dependencies
  have cleared.

## Block Lifecycle Integration
- The sequencer coordinator lifts the miner into a slot-based workflow. `NotifySlotStart`, `NotifyRequestSeal`, and
  `OnBlockBuildingComplete` (all in `eth/api_backend.go`) maintain a "pending submission" block, stage the mandatory
  `clear()` transaction when the SP requests sealing, and push the final L2 block back to the SP via `sendStoredL2Block`.
- Once the SP acknowledges inclusion (`Decided` message), `ClearSequencerTransactionsAfterBlock` flushes staged
  transactions and resets the coordinator state ready for the next slot.

## CLI Helpers
- `cmd/xclient` and `cmd/xbridge` are small harnesses that build ping/pong xTs against the mailbox contract, encode them
  as `XTRequest` messages, and submit them via `eth_sendXTransaction`. They are useful for local smoke testing and
  demonstrate how to craft multi-chain payloads.

## Embedded Shared Publisher SDK
- The `internal/rollup-shared-publisher` module vendors the SP proto definitions, 2PC coordinator, transport layer, and
  SBCP sequencer coordinator. This mirrors the external repo so the execution client can link against the same message
  types without importing from a private module. When making protocol changes, update both the vendored copy and the
  standalone `rollup-shared-publisher` repo.

## Key Files
- `internal/ethapi/api.go`: RPC plumbing (`SendXTransaction`).
- `eth/api_backend.go`: SP/coordinator wiring, SBCP callbacks, mailbox orchestration.
- `eth/ssv_mailbox_processor.go`: mailbox trace analysis, CIRC message dispatch, dependency fulfillment.
- `node/node.go`: bootstrap runtime that owns the SP client, peer transports, and sequencer coordinator lifecycle.
- `cmd/xclient`, `cmd/xbridge`: reference clients for building XT payloads.
