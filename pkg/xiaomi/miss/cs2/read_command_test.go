package cs2

import (
	"bytes"
	"testing"
)

func TestReadCommandBigEndian(t *testing.T) {
	c := &Conn{
		channels: [4]*dataChannel{
			newDataChannel(0, 1),
		},
	}

	c.channels[0].popBuf <- []byte{
		0x00, 0x00, 0x10, 0x01,
		0xAA, 0xBB,
	}

	cmd, data, err := c.ReadCommand()
	if err != nil {
		t.Fatal(err)
	}

	if cmd != 0x1001 {
		t.Fatalf("unexpected command: got 0x%x, want 0x1001", cmd)
	}

	if !bytes.Equal(data, []byte{0xAA, 0xBB}) {
		t.Fatalf("unexpected payload: %x", data)
	}
}
