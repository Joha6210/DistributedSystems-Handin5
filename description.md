# A Distributed Auction System

## Introduction

You must implement a **distributed auction system** using replication: a distributed component which handles auctions, and provides operations for bidding and querying the state of an auction. The component must faithfully implement the semantics described below, and must be resilient to at least **one (1) crash failure**.

---

## MA Learning Goal

The goal of this mandatory activity is that you learn (by doing) how to use replication to design a service that is resilient to crashes. In particular, it is important that you can recognise the key issues that may arise and understand how to deal with them.

---

## API

The system must be implemented as some number of nodes, running on **distinct processes** (no threads). Clients direct API requests to any node they happen to know (it is up to you to decide how many nodes can be known). Nodes must respond to the following API:

### `bid`

- **Inputs:** `amount` (an `int`)
- **Outputs:** `ack`
- **Comment:** Given a bid, returns an outcome among `{fail, success, exception}`.

### `result`

- **Inputs:** `void`
- **Outputs:** `outcome`
- **Comment:** If the auction is over, returns the result; otherwise returns the highest current bid.

---

## Semantics

Your component must have the following behaviour, for any reasonable sequentialisation/interleaving of requests:

- The **first call** to `bid` registers the bidder.
- Bidders can bid **several times**, but each new bid must be **higher** than the previous one(s).
- After a predefined timeframe, the **highest bidder** becomes the **winner** of the auction (e.g., after `100` time units from system start).
- Bidders can query the system to know the state of the auction.

---

## Faults

- Assume a network that provides **reliable, ordered message transport**, where transmissions to non-failed nodes complete within a known time-limit.
- Your component must be **resilient to the failure-stop failure of one (1) node**.

---

## Report

Write a 2-page report (up to 3 pages if necessary) containing the following structure (**use exactly these four section titles**):

### 1. Introduction

A short introduction to what you have done.

---

### 2. Architecture

A description of:

- The architecture of the system.
- The behaviour of its protocols.
- Any protocols used internally between nodes.

---

### 3. Correctness 1

Argue whether your implementation satisfies **linearisability** or **sequential consistency**.

You must:

1. **Precisely define** the chosen property.
2. **Argue** whether your implementation satisfies it.

---

### 4. Correctness 2

Provide an argument that your protocol is correct:

- In the absence of failures.
- In the presence of failures (specifically the single crash-stop fault model).

---
