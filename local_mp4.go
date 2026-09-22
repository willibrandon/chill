package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

// readMP4Numbers reads the 16-bit trkn/disk fields without loading audio or
// artwork. Only metadata container paths are traversed; sizes are checked
// against their parent and the number of inspected atoms is bounded.
func readMP4Numbers(source io.ReaderAt, size int64) (track, disc int, err error) {
	remaining := 10000
	var walk func(start, end int64, parent string) error
	walk = func(start, end int64, parent string) error {
		for at := start; at < end; {
			remaining--
			if remaining < 0 {
				return fmt.Errorf("too many MP4 metadata atoms")
			}
			if end-at < 8 {
				return io.ErrUnexpectedEOF
			}
			var header [16]byte
			if _, err := source.ReadAt(header[:8], at); err != nil {
				return err
			}
			length := uint64(binary.BigEndian.Uint32(header[:4]))
			name := string(header[4:8])
			headerSize := int64(8)
			switch length {
			case 0:
				length = uint64(end - at)
			case 1:
				if end-at < 16 {
					return io.ErrUnexpectedEOF
				}
				if _, err := source.ReadAt(header[8:], at+8); err != nil {
					return err
				}
				length = binary.BigEndian.Uint64(header[8:])
				headerSize = 16
			}
			if length < uint64(headerSize) || length > uint64(end-at) {
				return fmt.Errorf("invalid MP4 %s atom size", name)
			}
			payload, next := at+headerSize, at+int64(length)
			container := parent == "" && (name == "moov" || name == "meta") ||
				parent == "moov" && (name == "udta" || name == "meta") ||
				parent == "udta" && name == "meta" ||
				parent == "meta" && name == "ilst" ||
				parent == "ilst" && (name == "trkn" || name == "disk")
			if container {
				if name == "meta" {
					if next-payload < 4 {
						return io.ErrUnexpectedEOF
					}
					payload += 4 // FullBox version and flags precede its child atoms.
				}
				if err := walk(payload, next, name); err != nil {
					return err
				}
			} else if name == "data" && (parent == "trkn" || parent == "disk") {
				// Eight bytes of type/locale, then reserved, number, total
				// as big-endian uint16 values (and optional trailing padding).
				var data [14]byte
				if next-payload < int64(len(data)) {
					return io.ErrUnexpectedEOF
				}
				if _, err := source.ReadAt(data[:], payload); err != nil {
					return err
				}
				if binary.BigEndian.Uint32(data[:4]) == 0 {
					number := int(binary.BigEndian.Uint16(data[10:12]))
					if parent == "trkn" {
						track = number
					} else {
						disc = number
					}
				}
			}
			at = next
		}
		return nil
	}
	err = walk(0, size, "")
	return track, disc, err
}
