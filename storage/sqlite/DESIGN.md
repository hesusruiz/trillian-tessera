# MySQL Design

Author: [Jesus Ruiz](https://github.com/hesusruiz), based heavily on the MySQL doc by [Martin Hutchinson](https://github.com/mhutchinson)

This document describes how the storage implementation for running Tessera on SQLite is intended to work.

## Intended Audience

- Cloud neutral deployments
- On-premise deployments

## Architecture

The DB layout has been designed such that serving any read request is a point lookup. This can be framed as each row in the database maps to a single file that would be created in the file-based implementations of Tessera.

### Table Schema

#### `Checkpoint`

A single row that records the current published checkpoint.

#### `TreeState`

A single row that records the current state of the tree. Updated after every integration.

#### `Subtree`

An internal tile consisting of hashes. There is one row for each internal tile, and this is updated until it is completed, at which point it is immutable.

#### `TiledLeaves`

The data committed to by the leaves of the tree. Follows the same evolution as Subtree.
 
Reads can scale horizontally with very little overhead or contention between frontends. In a simple deployment with a single process accessing the SQLite database, multiple concurrent goroutines can read from the database as per the SQLite mechanism. It is possible to have multiple processes reading from the same SQLite database file.

Writing is more complicated than reading for the following reasons:

1. Each write request updates multiple rows across all tables.
1. Write requests need to be globally coordinated to ensure every sequence number is allocated precisely once.

There can be only one process writing to the SQLite database, with many frontends writing to the database through the API provided by the process 'owning' the database. The frontends can not write directly using the SQL driver. The performance of such approach may be reasonable for some deployments, when the required aggregated write performance is enough (initial testing indicates tens of thousands of writes/sec can be easily achieved).

The structure is essentially the same as the MySQL implementation.

API Handlers:

1. Handle the /add request, and extract the data to be added
1. Add the data to a pool to be sequenced and block until this returns the index

Sequence pool:

1. Accept requests from API handlers to add a new entry to the pool.
1. If the pool is now of the maximum size, flush the current batch.
   1. If this is the first entry in the batch, then start a timer and flush the batch after a timeout.
1. Flushing: starts a sequence & integrate operation.

Sequence & integrate (DB integration starts here):

TODO: no need to take the write lock on the `TreeState` row, and is it enough with a native Go write mutex.

1. Takes a batch of entries to sequence and integrate
1. Starts a transaction, which first takes a write lock on the `TreeState` row to ensure that:
   1. No other processes will be competing with this work.
   1. That the next index to sequence is known (this is the same as the current tree size)
1. Update the required TiledLeaves rows
1. Perform an integration operation to update the Merkle tree, updating/adding Subtree rows as needed, and eventually updating the `TreeState` row
1. Commit the transaction
1. Checkpoints representing the latest state of the tree are published at the configured interval.

## Costs

Either all the money, or free. This could run as lightly as fitting inside a free-tier GCE VM, or scale up to a Cloud SQL instance that costs a hefty sum each month. These prices could be estimated based on QPS. It is a lot harder to estimate the price when physical machines are owned in an on-prem deployment.

## Alternative Considered

For this implementation, MySQL 8 was picked as the RDBMS. Other options considered were PostgreSQL and CockroachDB. The choice was somewhat arbitrary, and any of these solutions could be reasonably justified. My justification for picking MySQL is that it is more ubiquitous than Cockroach, and [writes scale better than PostgreSQL](https://www.uber.com/en-GB/blog/postgres-to-mysql-migration/). The implementation uses pretty standard SQL, so it isn’t envisaged that switching implementation would be insurmountable. That said, testing has only been performed with MySQL 8.
