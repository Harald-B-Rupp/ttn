// Copyright © 2015 The Things Network
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package components

import (
	"time"

	"github.com/TheThingsNetwork/ttn/core"
	. "github.com/TheThingsNetwork/ttn/core/errors"
	"github.com/TheThingsNetwork/ttn/utils/errors"
	"github.com/boltdb/bolt"
	"github.com/brocaar/lorawan"
)

// HandlerStorage manages the internal persistent state of a handler
type HandlerStorage interface {
	// Close properly ends the connection to the internal database
	Close() error

	// Lookup retrieves all entries associated to a given device
	Lookup(devAddr lorawan.DevAddr) ([]handlerEntry, error)

	// Reset removes all entries stored in the storage
	Reset() error

	// Store creates a new entry and add it to the other entries (if any)
	Store(devAddr lorawan.DevAddr, entry handlerEntry) error

	// Partition split the packets given in argument in multiple set, each associated to a single
	// device of a single app. Because packets may have the same address, the only way to
	// distinguish them is to directly look at the network session key associated to each packet.
	Partition(packet ...core.Packet) ([]handlerPartition, error)
}

type handlerBoltStorage struct {
	*bolt.DB
}

// handlerEntry stores all information that link an application to a device
type handlerEntry struct {
	AppEUI  lorawan.EUI64     // The application EUI
	AppSKey lorawan.AES128Key // The application session key
	DevAddr lorawan.DevAddr   // The device address
	NwkSKey lorawan.AES128Key // The network session key
}

// handlerPartition are generated by the partition method. See that method for more details
type handlerPartition struct {
	handlerEntry               // An actual handler entry
	Id           partitionId   // The id of that partition
	Packets      []core.Packet // Packet that are part of that partition
}

type partitionId [12]byte // AppEUI(8) | DevAddr(4)

// NewHandlerStorage creates a new bolt handler in-memory storage
func NewHandlerStorage() (HandlerStorage, error) {
	db, err := bolt.Open("handler_storage.db", 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}

	if err := initDB(db, "applications"); err != nil {
		return nil, err
	}

	return &handlerBoltStorage{DB: db}, nil
}

// Lookup implements the handlerStorage interface
func (s handlerBoltStorage) Lookup(devAddr lorawan.DevAddr) ([]handlerEntry, error) {
	entries, err := lookup(s.DB, "applications", devAddr, &handlerEntry{})
	if err != nil {
		return nil, err
	}
	return entries.([]handlerEntry), nil
}

// Store implements the handlerStorage interface
func (s handlerBoltStorage) Store(devAddr lorawan.DevAddr, entry handlerEntry) error {
	return store(s.DB, "applications", devAddr, &entry)
}

// Partition implements the handlerStorage interface
func (s handlerBoltStorage) Partition(packets ...core.Packet) ([]handlerPartition, error) {
	// Create a map in order to do the partition
	partitions := make(map[partitionId]handlerPartition)

	for _, packet := range packets {
		// First, determine devAddr, mandatory
		devAddr, err := packet.DevAddr()
		if err != nil {
			return nil, errors.New(ErrInvalidStructure, err)
		}

		entries, err := s.Lookup(devAddr)
		if err != nil {
			return nil, err
		}

		// Now get all tuples associated to that device address, and choose the right one
		for _, entry := range entries {
			// Compute MIC check to find the right keys
			ok, err := packet.Payload.ValidateMIC(entry.NwkSKey)
			if err != nil || !ok {
				continue // These aren't the droids you're looking for
			}

			// #Easy
			var id partitionId
			copy(id[:8], entry.AppEUI[:])
			copy(id[8:], entry.DevAddr[:])
			partitions[id] = handlerPartition{
				handlerEntry: entry,
				Id:           id,
				Packets:      append(partitions[id].Packets, packet),
			}
			break // We shouldn't look for other entries, we've found the right one
		}
	}

	// Transform the map to a slice
	res := make([]handlerPartition, 0, len(partitions))
	for _, p := range partitions {
		res = append(res, p)
	}

	if len(res) == 0 {
		return nil, errors.New(ErrNotFound, "")
	}

	return res, nil
}

// Close implements the handlerStorage interface
func (s handlerBoltStorage) Close() error {
	return s.DB.Close()
}

// Reset implements the handlerStorage interface
func (s handlerBoltStorage) Reset() error {
	return resetDB(s.DB, "applications")
}

// MarshalBinary implements the storageEntry interface
func (entry handlerEntry) MarshalBinary() ([]byte, error) {
	w := newEntryReadWriter(nil)
	w.Write(entry.AppEUI)
	w.Write(entry.AppSKey)
	w.Write(entry.DevAddr)
	w.Write(entry.NwkSKey)
	return w.Bytes()
}

// UnmarshalBinary implements the storageEntry interface
func (entry *handlerEntry) UnmarshalBinary(data []byte) error {
	if entry == nil || len(data) < 4 {
		return errors.New(ErrInvalidStructure, "Invalid handler entry")
	}
	r := newEntryReadWriter(data)
	r.Read(func(data []byte) { copy(entry.AppEUI[:], data) })
	r.Read(func(data []byte) { copy(entry.AppSKey[:], data) })
	r.Read(func(data []byte) { copy(entry.DevAddr[:], data) })
	r.Read(func(data []byte) { copy(entry.NwkSKey[:], data) })
	return r.Err()
}
