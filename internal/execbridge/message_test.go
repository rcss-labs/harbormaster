package execbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Fixture A: input_stream_data, seq 0, flags 0, CreatedDate 1761900000000,
// MessageId 00112233-4455-6677-8899-aabbccddeeff, PayloadType Output(1), Payload "ls\r"
const fixtureInputLsHex = "" +
	"00000074696e7075745f73747265616d5f646174612020202020202020202020" +
	"20202020000000010000019a396c730000000000000000000000000000000000" +
	"8899aabbccddeeff00112233445566771d887ce0f8672e4914d9000e801cd74e" +
	"cd805dd9366c0bc42dc16adc0197dc2f00000001000000036c730d"

// Fixture B: output_stream_data, seq 0, flags 1 (SYN), CreatedDate 1761900000500,
// MessageId 0f0e0d0c-0b0a-0908-0706-050403020100, PayloadType HandshakeRequest(5),
// Payload {"AgentVersion":"3.3.1230.0","RequestedClientActions":[{"ActionType":"SessionType",
//
//	"ActionParameters":{"SessionType":"Standard_Stream","Properties":null}}]}
const fixtureHandshakeHex = "" +
	"000000746f75747075745f73747265616d5f6461746120202020202020202020" +
	"20202020000000010000019a396c74f400000000000000000000000000000001" +
	"07060504030201000f0e0d0c0b0a0908fbb2e67c20f852cd2ff966441d14991c" +
	"15efdff6e74d500182244250a55579fe000000050000009c7b224167656e7456" +
	"657273696f6e223a22332e332e313233302e30222c2252657175657374656443" +
	"6c69656e74416374696f6e73223a5b7b22416374696f6e54797065223a225365" +
	"7373696f6e54797065222c22416374696f6e506172616d6574657273223a7b22" +
	"53657373696f6e54797065223a225374616e646172645f53747265616d222c22" +
	"50726f70657274696573223a6e756c6c7d7d5d7d"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex fixture: %v", err)
	}
	return b
}

func TestSerialize_InputMessage_MatchesGoldenBytes(t *testing.T) {
	m := ClientMessage{
		MessageType:   InputStreamMessage,
		SchemaVersion: 1,
		CreatedDate:   1761900000000,
		MessageID:     uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff"),
		PayloadType:   uint32(Output),
		Payload:       []byte("ls\r"),
	}
	got, err := m.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if len(got) != 123 {
		t.Errorf("length = %d, want 123", len(got))
	}
	if h := hex.EncodeToString(got); h != fixtureInputLsHex {
		t.Errorf("bytes mismatch\n got %s\nwant %s", h, fixtureInputLsHex)
	}
}

func TestDeserialize_HandshakeRequest_Fields(t *testing.T) {
	m, err := Deserialize(mustHex(t, fixtureHandshakeHex))
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if m.HeaderLength != 116 || m.MessageType != "output_stream_data" || m.SchemaVersion != 1 ||
		m.CreatedDate != 1761900000500 || m.SequenceNumber != 0 || m.Flags != 1 ||
		m.PayloadType != 5 || m.PayloadLength != 156 {
		t.Errorf("unexpected header: %+v", m)
	}
	if m.MessageID.String() != "0f0e0d0c-0b0a-0908-0706-050403020100" {
		t.Errorf("MessageID = %s", m.MessageID)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	req, err := m.HandshakeRequest()
	if err != nil {
		t.Fatalf("HandshakeRequest: %v", err)
	}
	if req.AgentVersion != "3.3.1230.0" {
		t.Errorf("AgentVersion = %q", req.AgentVersion)
	}
	if len(req.RequestedClientActions) != 1 || req.RequestedClientActions[0].ActionType != ActionSessionType {
		t.Errorf("actions = %+v", req.RequestedClientActions)
	}
}

func TestDeserialize_RejectsShortInput(t *testing.T) {
	full := mustHex(t, fixtureInputLsHex)
	for _, n := range []int{0, 3, 4, 50, 119} {
		if _, err := Deserialize(full[:n]); err == nil {
			t.Errorf("len %d: expected error", n)
		}
	}
}

func TestDeserialize_RejectsLyingHeaders(t *testing.T) {
	b := mustHex(t, fixtureInputLsHex)
	binary.BigEndian.PutUint32(b[0:4], 0xffffffff)
	if _, err := Deserialize(b); err == nil {
		t.Error("lying HeaderLength: expected error")
	}
	b = mustHex(t, fixtureInputLsHex)
	binary.BigEndian.PutUint32(b[116:120], 0xffffffff)
	if _, err := Deserialize(b); err == nil {
		t.Error("huge PayloadLength: expected error")
	}
}

func TestDeserialize_RejectsPayloadLengthMismatch(t *testing.T) {
	b := mustHex(t, fixtureInputLsHex)
	binary.BigEndian.PutUint32(b[116:120], 9)
	if _, err := Deserialize(b); err == nil {
		t.Error("expected error for PayloadLength mismatch")
	}
}

func TestValidate_RejectsBadDigest(t *testing.T) {
	b := mustHex(t, fixtureInputLsHex)
	b[len(b)-1] ^= 0xff
	m, err := Deserialize(b)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	err = m.Validate()
	if err == nil || !strings.Contains(err.Error(), "payload Hash is not valid") {
		t.Errorf("Validate error = %v", err)
	}
}

func TestValidate_SkipsPublicationMessages(t *testing.T) {
	for _, mt := range []string{StartPublicationMessage, PausePublicationMessage} {
		m := ClientMessage{MessageType: mt}
		if err := m.Validate(); err != nil {
			t.Errorf("%s: %v", mt, err)
		}
	}
}

func TestRoundTrip_UUIDHalvesSwapped(t *testing.T) {
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	m := ClientMessage{MessageType: InputStreamMessage, SchemaVersion: 1, CreatedDate: 5,
		SequenceNumber: -3, MessageID: id, PayloadType: uint32(Size), Payload: []byte(`{"cols":80,"rows":24}`)}
	raw, err := m.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw[64:72], id[8:16]) || !bytes.Equal(raw[72:80], id[0:8]) {
		t.Errorf("uuid halves not swapped: % x", raw[64:80])
	}
	back, err := Deserialize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.MessageID != id || back.SequenceNumber != -3 || !bytes.Equal(back.Payload, m.Payload) {
		t.Errorf("round trip mismatch: %+v", back)
	}
}

func TestNewAck_PayloadAndFlags(t *testing.T) {
	orig, err := Deserialize(mustHex(t, fixtureHandshakeHex))
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1761900001000)
	ack := NewAck(orig, now)
	if ack.MessageType != AcknowledgeMessage || ack.Flags != 3 || ack.SequenceNumber != 0 || ack.SchemaVersion != 1 ||
		ack.CreatedDate != uint64(now.UnixMilli()) || ack.MessageID == uuid.Nil {
		t.Errorf("ack header: %+v", ack)
	}
	want := `{"AcknowledgedMessageType":"output_stream_data","AcknowledgedMessageId":"0f0e0d0c-0b0a-0908-0706-050403020100","AcknowledgedMessageSequenceNumber":0,"IsSequentialMessage":true}`
	if string(ack.Payload) != want {
		t.Errorf("payload = %s", ack.Payload)
	}
	// The ack must survive a wire round trip and decode back.
	raw, err := ack.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Deserialize(raw)
	if err != nil {
		t.Fatal(err)
	}
	content, err := back.AckContent()
	if err != nil || content.MessageID != orig.MessageID.String() || !content.IsSequentialMessage {
		t.Errorf("AckContent = %+v, %v", content, err)
	}
}

func TestNewInput_SetsTypeSchemaFlags(t *testing.T) {
	now := time.UnixMilli(1761900002345)
	m := NewInput(7, Output, []byte("x"), now)
	if m.MessageType != InputStreamMessage || m.SchemaVersion != 1 || m.Flags != 0 || m.SequenceNumber != 7 ||
		m.CreatedDate != uint64(now.UnixMilli()) || m.PayloadType != uint32(Output) || string(m.Payload) != "x" ||
		m.MessageID == uuid.Nil {
		t.Errorf("input: %+v", m)
	}
}

func TestOpenDataChannelInput_JSONTags(t *testing.T) {
	b, err := json.Marshal(OpenDataChannelInput{MessageSchemaVersion: "1.0", RequestID: "r", TokenValue: "t", ClientID: "c", ClientVersion: "v"})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]string
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"MessageSchemaVersion", "RequestId", "TokenValue", "ClientId", "ClientVersion"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("missing key %s in %s", k, b)
		}
	}
}
