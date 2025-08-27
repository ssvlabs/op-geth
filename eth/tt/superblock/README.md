# Superblock Construction Protocol (SBCP)

This package implements the Superblock Construction Protocol (SBCP), a system designed to coordinate the creation of "
superblocks" that bundle transactions from multiple independent rollups. Its primary goal is to enable synchronous
composability and atomic cross-chain transactions in a multi-rollup environment.

The protocol is orchestrated by a central node, the **Shared Publisher (SP)**, which manages a slot-based timeline.
Participating rollup **Sequencers** follow the SP's timeline to build and submit their individual L2 blocks, which are
then aggregated into a final superblock.

For a detailed formal specification of the protocol, please
see [Superblock Construction Protocol](./../../docs/superblock_construction_protocol.md).

## Core Concepts

- **Shared Publisher (SP):** The central coordinator node that orchestrates the entire protocol. The logic for the SP is
  primarily contained in `coordinator.go`.
- **Sequencer:** A node representing a participating rollup chain. It builds L2 blocks according to the SP's signals.
  The logic for a sequencer is contained in the `sequencer/` sub-package.
- **Slot:** A fixed-duration time interval (12 seconds, aligned with Ethereum slots) during which transactions are
  collected and a superblock is proposed. The `slot/` package manages this timing and the associated state machine.
- **Superblock:** A block that contains a collection of L2 blocks from different rollups for a specific slot, along with
  metadata about included cross-chain transactions.
- **Cross-Chain Transaction (XT):** A transaction involving operations on multiple rollups, which must be executed
  atomically.
- **Synchronous Composability Protocol (SCP):** The underlying consensus mechanism (a 2-Phase Commit protocol) used to
  agree on the outcome (commit or abort) of a cross-chain transaction across all participating sequencers. The core
  logic for this is in the `x/consensus` package.

## Architecture Deep Dive

The `x/superblock` package is organized into sub-packages, each with a distinct responsibility.

- **`/` (Root):**
    - `coordinator.go`: The heart of the Shared Publisher. It drives the slot lifecycle, orchestrates SCP instances for
      cross-chain transactions, and ultimately builds the final superblock.
    - `interfaces.go`: Defines the core interfaces for the SP and slot management.

- **`adapter/`:**
    - A crucial bridge package. `publisher_wrapper.go` wraps a generic P2P publisher (from `x/publisher`) and injects
      the SBCP logic, turning a standard node into a Shared Publisher.

- **`handlers/`:**
    - Implements a Chain of Responsibility pattern for processing incoming P2P messages. This allows for clean
      separation of message-handling logic specific to SBCP.

- **`sequencer/`:**
    - Contains the complete logic for a participating **Sequencer** node. This includes its own state machine (
      `Waiting` -> `Building-Free` -> `Building-Locked` -> `Submission`), a block builder, and logic for following the
      SP's commands.

- **`slot/`:**
    - `manager.go`: A clean, focused time manager for calculating slot boundaries and progress.
    - `state_machine.go`: Implements the SP's core state machine (`Starting` → `Free` → `Locked` → `Sealing`), which
      governs the protocol's execution phases within a slot.

- **`store/`, `queue/`, `wal/`:**
    - These packages define interfaces for persistence and data management. They provide in-memory implementations for
      testing, with the expectation that production-level backends will be implemented.

- **`l1/` & `registry/`:**
    - Handle interaction with the L1 blockchain. `l1/` is responsible for publishing the final superblock. `registry/`
      is responsible for discovering and tracking the set of active rollups.

## Message and Consensus Handling

The architecture uses a layered approach to handle network messages and consensus, which allows for both a simple
standalone mode and the full SBCP mode.

1. **Consensus Engine (`x/consensus`):** Provides the core, reusable 2-Phase Commit (2PC) logic. It is not a network
   handler itself but a library that is driven by a consumer and reports outcomes via callbacks.
2. **Base Publisher (`x/publisher`):** Provides a basic publisher that can run a simple 2PC for cross-chain
   transactions. It has its own simple handler for `Vote` and `XTRequest` messages.
3. **SBCP Adapter (`x/superblock/adapter`):** This is the key to enabling SBCP mode. It wraps the base publisher and
   intercepts the entire flow:
    - **Message Interception:** It replaces the transport's message handler with its own `HandlerChain`. This chain
      processes SBCP-specific messages (like `L2Block`) first.
    - **Fallback:** Any message not handled by the SBCP chain (like a `Vote`) is passed down to the base `publisher`'s
      original handler, which then feeds it to the consensus engine.
    - **Callback Interception:** It overrides the consensus engine's decision callback. This allows the SBCP layer to be
      notified of a transaction's outcome, so it can update its own slot state machine accordingly (e.g., moving from
      `Locked` back to `Free`).

This powerful composition pattern allows the `superblock` module to add its complex, stateful logic on top of a simpler,
generic consensus and publisher layer.

## Protocol Flow (Happy Path)

A single slot follows the sequence defined in the protocol specification:

1. **Slot Start:** The SP's `Coordinator` begins a new slot and broadcasts a `StartSlot` message.
2. **Block Building:** Sequencers receive `StartSlot`, transition to the `Building-Free` state, and begin constructing
   their L2 blocks.
3. **Cross-Chain Transaction (XT):**
   a. An `XTRequest` is submitted to the SP, which places it in a priority queue.
   b. The SP takes a request from the queue, transitions its state to `Locked`, and initiates SCP by broadcasting a
   `StartSC` message.
   c. Sequencers transition to `Building-Locked`, validate their part of the XT, and send a `Vote` to the SP.
   d. The SP gathers votes and broadcasts a `Decided` message.
   e. Sequencers commit or abort the XT in their draft blocks and transition back to `Building-Free`.
4. **Seal Request:** At the seal cutover point (e.g., 8/12 seconds), the SP transitions to `Sealing` and broadcasts a
   `RequestSeal` message with the list of all included XTs for the slot.
5. **L2 Block Submission:** Sequencers transition to `Submission`, finalize their L2 blocks, and send the completed
   `L2Block` to the SP.
6. **Superblock Creation:** The SP gathers all L2 blocks, validates them, assembles the final `Superblock`, and
   publishes it to the L1 chain.
7. **Next Slot:** The SP transitions back to `StartingSlot` to begin the cycle anew.

## Refactoring and Future Work

This package is under active development. Key areas for improvement include:

- **Production-Grade Dependencies:** The current implementation relies on in-memory mocks for storage, queuing, and L1
  interaction. These must be replaced with robust, production-ready implementations (e.g., using a database like
  BadgerDB and a real Ethereum client).
- **Decoupling via Dependency Injection:** The `adapter/publisher_wrapper.go` needs to be refactored to accept its
  dependencies via interfaces, rather than instantiating them directly. This is the highest-priority task to make the
  system configurable and testable.
- **Coordinator Refactoring:** The main `Coordinator` is a "God object" that should be broken down into smaller, more
  focused components (e.g., `SlotLifecycleManager`, `SuperblockBuilder`) to improve maintainability and testability.
- **Unify Handling Logic:** The current mix of a `HandlerChain`, a fallback handler, and intercepted callbacks is
  functional but complex. This could be refactored into a single, unified message router at the adapter level to make
  the flow of control more explicit.
- **Comprehensive Testing:** A full suite of unit and integration tests is required to ensure the protocol's correctness
  and stability.
- **WAL Recovery:** The Write-Ahead Log (WAL) recovery path needs to be fully implemented to ensure the system can
  recover gracefully from a crash, as outlined in the protocol specification.
- **Parallelism Optimizations:** The protocol specification describes opportunities for advanced parallelism (e.g.,
  processing local transactions during the `Building-Locked` state). This is a significant future optimization.
