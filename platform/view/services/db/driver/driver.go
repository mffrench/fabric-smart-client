/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package driver

import (
	"github.com/hyperledger-labs/fabric-smart-client/platform/common/driver"
	"github.com/hyperledger-labs/fabric-smart-client/platform/common/utils/collections"
	"github.com/pkg/errors"
)

var (
	// UniqueKeyViolation happens when we try to insert a record with a conflicting unique key (e.g. replicas)
	UniqueKeyViolation = errors.New("unique key violation")
	// DeadlockDetected happens when two transactions are taking place at the same time and interact with the same rows
	DeadlockDetected = errors.New("deadlock detected")
)

type SQLError = error

type VersionedValue = driver.VersionedValue

type VersionedMetadataValue = driver.VersionedMetadataValue

type UnversionedRead struct {
	Key driver.PKey
	Raw driver.RawValue
}

type UnversionedResultsIterator = collections.Iterator[*UnversionedRead]

type UnversionedValue = driver.RawValue

type VersionedRead = driver.VersionedRead

type VersionedResultsIterator = collections.Iterator[*VersionedRead]

type QueryExecutor = driver.QueryExecutor

// SQLErrorWrapper transforms the different errors returned by various SQL implementations into an SQLError that is common
type SQLErrorWrapper interface {
	WrapError(error) error
}

type BasePersistence[V any, R any] interface {
	// SetState sets the given value for the given namespace, key, and version
	SetState(namespace driver.Namespace, key driver.PKey, value V) error
	// SetStates sets the given values for the given namespace, key, and version
	SetStates(namespace driver.Namespace, kvs map[driver.PKey]V) map[driver.PKey]error
	// GetState gets the value and version for given namespace and key
	GetState(namespace driver.Namespace, key driver.PKey) (V, error)
	// DeleteState deletes the given namespace and key
	DeleteState(namespace driver.Namespace, key driver.PKey) error
	// DeleteStates deletes the given namespace and keys
	DeleteStates(namespace driver.Namespace, keys ...driver.PKey) map[driver.PKey]error
	// GetStateRangeScanIterator returns an iterator that contains all the key-values between given key ranges.
	// startKey is included in the results and endKey is excluded. An empty startKey refers to the first available key
	// and an empty endKey refers to the last available key. For scanning all the keys, both the startKey and the endKey
	// can be supplied as empty strings. However, a full scan should be used judiciously for performance reasons.
	// The returned VersionedResultsIterator contains results of type *VersionedRead.
	GetStateRangeScanIterator(namespace driver.Namespace, startKey, endKey driver.PKey) (collections.Iterator[*R], error)
	// GetStateSetIterator returns an iterator that contains all the values for the passed keys.
	// The order is not respected.
	GetStateSetIterator(ns driver.Namespace, keys ...driver.PKey) (collections.Iterator[*R], error)
	// Close closes this persistence instance
	Close() error
	// BeginUpdate starts the session
	BeginUpdate() error
	// Commit commits the changes since BeginUpdate
	Commit() error
	// Discard discards the changes since BeginUpdate
	Discard() error
	// Stats returns driver specific statistics of the datastore
	Stats() any
}

// UnversionedPersistence models a key-value storage place
type UnversionedPersistence interface {
	BasePersistence[UnversionedValue, UnversionedRead]
}

// VersionedPersistence models a versioned key-value storage place
type VersionedPersistence interface {
	BasePersistence[VersionedValue, VersionedRead]
	// GetStateMetadata gets the metadata and version for given namespace and key
	GetStateMetadata(namespace driver.Namespace, key driver.PKey) (driver.Metadata, driver.RawVersion, error)
	// SetStateMetadata sets the given metadata for the given namespace, key, and version
	SetStateMetadata(namespace driver.Namespace, key driver.PKey, metadata driver.Metadata, version driver.RawVersion) error
	// SetStateMetadatas sets the given metadata for the given namespace, keys, and version
	SetStateMetadatas(ns driver.Namespace, kvs map[driver.PKey]driver.VersionedMetadataValue) map[driver.PKey]error
}

type WriteTransaction interface {
	// SetState sets the given value for the given namespace, key, and version
	SetState(namespace driver.Namespace, key driver.PKey, value VersionedValue) error
	// DeleteState deletes the given namespace and key
	DeleteState(namespace driver.Namespace, key driver.PKey) error
	// Commit commits the changes since BeginUpdate
	Commit() error
	// Discard discards the changes since BeginUpdate
	Discard() error
}

type UnversionedWriteTransaction interface {
	// SetState sets the given value for the given namespace, key
	SetState(namespace driver.Namespace, key driver.PKey, value UnversionedValue) error
	// DeleteState deletes the given namespace and key
	DeleteState(namespace driver.Namespace, key driver.PKey) error
	// Commit commits the changes since BeginUpdate
	Commit() error
	// Discard discards the changes since BeginUpdate
	Discard() error
}

type TransactionalVersionedPersistence interface {
	VersionedPersistence

	NewWriteTransaction() (WriteTransaction, error)
}

type TransactionalUnversionedPersistence interface {
	UnversionedPersistence

	NewWriteTransaction() (UnversionedWriteTransaction, error)
}

// Config provides access to the underlying configuration
type Config interface {
	// IsSet checks to see if the key has been set in any of the data locations
	IsSet(key string) bool
	// UnmarshalKey takes the value corresponding to the passed key and unmarshals it into the passed structure
	UnmarshalKey(key string, rawVal interface{}) error
}

type NamedDriver = driver.NamedDriver[Driver]

type Driver interface {
	// NewTransactionalVersioned returns a new TransactionalVersionedPersistence for the passed data source and config
	NewTransactionalVersioned(dataSourceName string, config Config) (TransactionalVersionedPersistence, error)
	// NewVersioned returns a new VersionedPersistence for the passed data source and config
	NewVersioned(dataSourceName string, config Config) (VersionedPersistence, error)
	// NewUnversioned returns a new UnversionedPersistence for the passed data source and config
	NewUnversioned(dataSourceName string, config Config) (UnversionedPersistence, error)
	// NewTransactionalUnversioned returns a new TransactionalUnversionedPersistence for the passed data source and config
	NewTransactionalUnversioned(dataSourceName string, config Config) (TransactionalUnversionedPersistence, error)
}

type (
	ColumnKey       = string
	TriggerCallback func(Operation, map[ColumnKey]string)
	Operation       int
)

const (
	Unknown Operation = iota
	Delete
	Insert
	Update
)

type Notifier interface {
	// Subscribe registers a listener for when a value is inserted/updated/deleted in the given table
	Subscribe(callback TriggerCallback) error
	// UnsubscribeAll removes all registered listeners for the given table
	UnsubscribeAll() error
}

type UnversionedNotifier interface {
	UnversionedPersistence
	Notifier
}
type VersionedNotifier interface {
	VersionedPersistence
	Notifier
}
