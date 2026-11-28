package gateway

import (
	"bytes"
	"encoding/binary"
	"remote-codex/internal/embedassets"
	"sync"
	"testing"
)

func TestBridgeJournalReplayAndDuplicateDelivery(t *testing.T) {
	journal := newBridgeJournal()
	payload := embedassets.Read("browser.js")
	if err := journal.append(payload); err != nil {
		t.Fatal(err)
	}
	first := journal.next(0)
	if first == nil || first.sequence != 1 || !bytes.Equal(first.data[4:], payload) {
		t.Fatal("initial frame changed")
	}
	if replay := journal.next(0); !bytes.Equal(replay.data, first.data) {
		t.Fatal("resume must replay unacknowledged data")
	}
	if err := journal.ack(2); err == nil {
		t.Fatal("future acknowledgement accepted")
	}
	if err := journal.ack(1); err != nil {
		t.Fatal(err)
	}
	if journal.next(0) != nil || journal.bytes != 0 {
		t.Fatal("acknowledged bytes retained")
	}
	var delivered bytes.Buffer
	for range 2 {
		seq, err := journal.receive(first.data, func(data []byte) error { _, err := delivered.Write(data); return err })
		if err != nil || seq != 1 {
			t.Fatal(seq, err)
		}
	}
	if !bytes.Equal(delivered.Bytes(), payload) {
		t.Fatal("duplicate delivery after reconnect")
	}
	gap := bytes.Clone(first.data)
	binary.BigEndian.PutUint32(gap, 3)
	if _, err := journal.receive(gap, func(data []byte) error { _, err := delivered.Write(data); return err }); err == nil {
		t.Fatal("sequence gap accepted")
	}
}

func TestBridgeJournalConcurrentAcknowledgements(t *testing.T) {
	journal := newBridgeJournal()
	payload := embedassets.Read("mobile.css")
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 50 {
				if err := journal.append(payload); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	workers.Wait()
	var cursor uint32
	for frame := journal.next(cursor); frame != nil; frame = journal.next(cursor) {
		cursor = frame.sequence
		if err := journal.ack(cursor); err != nil {
			t.Fatal(err)
		}
	}
	if cursor != 400 || journal.bytes != 0 {
		t.Fatal(cursor, journal.bytes)
	}
}
