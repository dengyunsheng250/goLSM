package golsm

import (
	"io"
	"os"
)

// Alias for int64 size
type EntrySize int64

type SSTable struct {
	index      *Index    // Index of the SSTable
	file       *os.File  // File handle for the on-disk SSTable file.
	dataOffset EntrySize // Offset from where the actual entries begin
}

// Writes a list of MemtableKeyValue to a file in SSTable format.
// Format of the file is:
// 1. Size of the index (OffsetSize)
// 2. Index data (Index Protobuf)
// 3. Entries data (MemtableKeyValue Protobuf)
//
// The entries data is written in as:
// 1. Size of the entry (OffsetSize)
// 2. Entry data (MemtableKeyValue Protobuf)
//
// The index is a list of IndexEntry, which is a struct containing the key and
// the offset of the entry in the file (after the index).
func SerializeToSSTable(messages []*MemtableKeyValue, filename string) (*SSTable, error) {
	index, entriesBuffer, err := buildIndexAndEntriesBuffer(messages)
	if err != nil {
		return nil, err
	}

	indexData := mustMarshal(index)

	dataOffset, err := writeSSTable(filename, indexData, entriesBuffer)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	return &SSTable{index: index, file: file, dataOffset: dataOffset}, nil
}

// Opens an SSTable file for reading and returns a handle to it.
func OpenSSTable(filename string) (*SSTable, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	index, dataOffset, err := readSSTableMetadata(file)
	if err != nil {
		return nil, err
	}

	return &SSTable{index: index, file: file, dataOffset: dataOffset}, nil
}

func (s *SSTable) Close() error {
	return s.file.Close()
}

// Reads the value for a given key from the SSTable. Returns nil if the key is
// not found.
func (s *SSTable) Get(key string) ([]byte, error) {
	offset, found := findOffsetForKey(s.index.Entries, key)
	if !found {
		return nil, nil
	}

	// Seek to the offset of the entry in the file. The offset is relative to the
	// start of the entries data therefore we add the dataOffset to it.
	if _, err := s.file.Seek(int64(offset)+int64(s.dataOffset), io.SeekStart); err != nil {
		return nil, err
	}

	size, err := readDataSize(s.file)
	if err != nil {
		return nil, err
	}

	data, err := readEntryDataFromFile(s.file, size)
	if err != nil {
		return nil, err
	}

	entry := &MemtableKeyValue{}
	mustUnmarshal(data, entry)

	// If the entry is a tombstone, return nil
	if entry.GetValue().GetCommand() == Command_DELETE {
		return nil, nil
	}

	return entry.GetValue().GetValue(), nil
}

// RangeScan returns all the values in the SSTable between startKey and endKey
// inclusive.
func (s *SSTable) RangeScan(startKey string, endKey string) ([][]byte, error) {
	startOffset, found := findStartOffsetForRangeScan(s.index.Entries, startKey)
	if !found {
		return nil, nil
	}

	if _, err := s.file.Seek(int64(startOffset)+int64(s.dataOffset), io.SeekStart); err != nil {
		return nil, err
	}

	var results [][]byte
	for {
		size, err := readDataSize(s.file)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		data, err := readEntryDataFromFile(s.file, size)
		if err != nil {
			return nil, err
		}

		entry := &MemtableKeyValue{}
		mustUnmarshal(data, entry)

		if entry.Key > endKey {
			break
		}

		if entry.GetValue().GetCommand() == Command_DELETE {
			continue
		}

		results = append(results, entry.GetValue().GetValue())
	}

	return results, nil
}