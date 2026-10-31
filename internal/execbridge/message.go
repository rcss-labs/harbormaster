// Copyright 2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// Ported to Harbormaster from github.com/aws/session-manager-plugin
// (src/message/clientmessage.go, messageparser.go, handshakemessage.go);
// modifications Copyright 2026 Harbormaster contributors, MIT licence.

package execbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// InputStreamMessage represents message type for input data
	InputStreamMessage = "input_stream_data"
	// OutputStreamMessage represents message type for output data
	OutputStreamMessage = "output_stream_data"
	// AcknowledgeMessage represents message type for acknowledge
	AcknowledgeMessage = "acknowledge"
	// ChannelClosedMessage represents message type for ChannelClosed
	ChannelClosedMessage = "channel_closed"
	// StartPublicationMessage notifies the client to start sending stream messages
	StartPublicationMessage = "start_publication"
	// PausePublicationMessage notifies the client to pause sending stream messages
	// as the remote data channel is inactive
	PausePublicationMessage = "pause_publication"
)

// PayloadType identifies how a message payload is to be interpreted.
type PayloadType uint32

const (
	Output               PayloadType = 1
	Error                PayloadType = 2
	Size                 PayloadType = 3
	Parameter            PayloadType = 4
	HandshakeRequest     PayloadType = 5
	HandshakeResponse    PayloadType = 6
	HandshakeComplete    PayloadType = 7
	EncChallengeRequest  PayloadType = 8
	EncChallengeResponse PayloadType = 9
	Flag                 PayloadType = 10
	StdErr               PayloadType = 11
	ExitCode             PayloadType = 12
)

// PayloadTypeFlag is the value carried in a payload of type Flag.
type PayloadTypeFlag uint32

const (
	DisconnectToPort   PayloadTypeFlag = 1
	TerminateSession   PayloadTypeFlag = 2
	ConnectToPortError PayloadTypeFlag = 3
)

// Header Flags bits. Acknowledge messages carry both (3).
const (
	FlagSYN uint64 = 1
	FlagFIN uint64 = 2
)

// ActionType used in the handshake to determine the action requested by the agent.
type ActionType string

const (
	ActionKMSEncryption ActionType = "KMSEncryption"
	ActionSessionType   ActionType = "SessionType"
)

// ActionStatus is the result of processing a requested client action.
type ActionStatus int

const (
	ActionSuccess     ActionStatus = 1
	ActionFailed      ActionStatus = 2
	ActionUnsupported ActionStatus = 3
)

// AcknowledgeContent informs the sender that a message has been received.
type AcknowledgeContent struct {
	MessageType         string `json:"AcknowledgedMessageType"`
	MessageID           string `json:"AcknowledgedMessageId"`
	SequenceNumber      int64  `json:"AcknowledgedMessageSequenceNumber"`
	IsSequentialMessage bool   `json:"IsSequentialMessage"`
}

// ChannelClosed informs the client to close the channel.
type ChannelClosed struct {
	MessageID     string `json:"MessageId"`
	CreatedDate   string `json:"CreatedDate"`
	DestinationID string `json:"DestinationId"`
	SessionID     string `json:"SessionId"`
	MessageType   string `json:"MessageType"`
	SchemaVersion int    `json:"SchemaVersion"`
	Output        string `json:"Output"`
}

// SizeData is the payload of a Size message (terminal resize).
type SizeData struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

// SessionTypeRequest contains the type of session to launch and its properties.
type SessionTypeRequest struct {
	SessionType string `json:"SessionType"`
	Properties  any    `json:"Properties"`
}

// HandshakeRequestPayload is sent by the agent to the client.
type HandshakeRequestPayload struct {
	AgentVersion           string                  `json:"AgentVersion"`
	RequestedClientActions []RequestedClientAction `json:"RequestedClientActions"`
}

// RequestedClientAction is an action requested by the agent of the client.
type RequestedClientAction struct {
	ActionType       ActionType      `json:"ActionType"`
	ActionParameters json.RawMessage `json:"ActionParameters"`
}

// ProcessedClientAction is the result of the client processing an action.
type ProcessedClientAction struct {
	ActionType   ActionType   `json:"ActionType"`
	ActionStatus ActionStatus `json:"ActionStatus"`
	ActionResult any          `json:"ActionResult"`
	Error        string       `json:"Error"`
}

// HandshakeResponsePayload is sent by the client in response to the handshake request.
type HandshakeResponsePayload struct {
	ClientVersion          string                  `json:"ClientVersion"`
	ProcessedClientActions []ProcessedClientAction `json:"ProcessedClientActions"`
	Errors                 []string                `json:"Errors"`
}

// HandshakeCompletePayload tells the client the handshake is complete.
type HandshakeCompletePayload struct {
	HandshakeTimeToComplete time.Duration `json:"HandshakeTimeToComplete"`
	CustomerMessage         string        `json:"CustomerMessage"`
}

// OpenDataChannelInput is the first (text) frame sent on the stream websocket.
type OpenDataChannelInput struct {
	MessageSchemaVersion string `json:"MessageSchemaVersion"`
	RequestID            string `json:"RequestId"`
	TokenValue           string `json:"TokenValue"`
	ClientID             string `json:"ClientId"`
	ClientVersion        string `json:"ClientVersion"`
}

// ClientMessage is a message sent to or received from the data channel.
//
//	| HL|         MessageType           |Ver|  CD   |  Seq  | Flags |
//	|         MessageId                     |           Digest              | PayType | PayLen|
//	|         Payload                       |
type ClientMessage struct {
	HeaderLength   uint32
	MessageType    string
	SchemaVersion  uint32
	CreatedDate    uint64
	SequenceNumber int64
	Flags          uint64
	MessageID      uuid.UUID
	PayloadDigest  []byte
	PayloadType    uint32
	PayloadLength  uint32
	Payload        []byte
}

const (
	hlLength             = 4
	messageTypeLength    = 32
	schemaVersionLength  = 4
	createdDateLength    = 8
	sequenceNumberLength = 8
	flagsLength          = 8
	messageIDLength      = 16
	payloadDigestLength  = 32
	payloadTypeLength    = 4
	payloadLengthLength  = 4

	hlOffset             = 0
	messageTypeOffset    = hlOffset + hlLength
	schemaVersionOffset  = messageTypeOffset + messageTypeLength
	createdDateOffset    = schemaVersionOffset + schemaVersionLength
	sequenceNumberOffset = createdDateOffset + createdDateLength
	flagsOffset          = sequenceNumberOffset + sequenceNumberLength
	messageIDOffset      = flagsOffset + flagsLength
	payloadDigestOffset  = messageIDOffset + messageIDLength
	payloadTypeOffset    = payloadDigestOffset + payloadDigestLength
	payloadLengthOffset  = payloadTypeOffset + payloadTypeLength
	payloadOffset        = payloadLengthOffset + payloadLengthLength

	// headerLengthValue is what the header-length field always contains: the
	// offset of the PayloadLength field.
	headerLengthValue = payloadLengthOffset
	schemaVersion     = 1
)

// Serialize encodes the message into its wire format. HeaderLength, PayloadLength
// and PayloadDigest are derived from the message, not taken from the struct.
func (m *ClientMessage) Serialize() ([]byte, error) {
	if len(m.MessageType) > messageTypeLength {
		return nil, fmt.Errorf("message type %q longer than %d bytes", m.MessageType, messageTypeLength)
	}
	out := make([]byte, payloadOffset+len(m.Payload))
	binary.BigEndian.PutUint32(out[hlOffset:], headerLengthValue)

	typeField := out[messageTypeOffset : messageTypeOffset+messageTypeLength]
	for i := range typeField {
		typeField[i] = ' '
	}
	copy(typeField, m.MessageType)

	binary.BigEndian.PutUint32(out[schemaVersionOffset:], m.SchemaVersion)
	binary.BigEndian.PutUint64(out[createdDateOffset:], m.CreatedDate)
	binary.BigEndian.PutUint64(out[sequenceNumberOffset:], uint64(m.SequenceNumber))
	binary.BigEndian.PutUint64(out[flagsOffset:], m.Flags)

	// The UUID is stored least-significant 8 bytes first.
	copy(out[messageIDOffset:], m.MessageID[8:16])
	copy(out[messageIDOffset+8:], m.MessageID[0:8])

	digest := sha256.Sum256(m.Payload)
	copy(out[payloadDigestOffset:], digest[:])

	binary.BigEndian.PutUint32(out[payloadTypeOffset:], m.PayloadType)
	binary.BigEndian.PutUint32(out[payloadLengthOffset:], uint32(len(m.Payload)))
	copy(out[payloadOffset:], m.Payload)
	return out, nil
}

// Deserialize decodes a wire-format message. Unlike upstream it checks every
// length first, so short or inconsistent input returns an error instead of panicking.
func Deserialize(b []byte) (ClientMessage, error) {
	var m ClientMessage
	if len(b) < payloadOffset {
		return m, fmt.Errorf("message too short: %d bytes, need at least %d", len(b), payloadOffset)
	}
	m.HeaderLength = binary.BigEndian.Uint32(b[hlOffset:])
	if m.HeaderLength != headerLengthValue {
		return m, fmt.Errorf("unexpected header length %d, want %d", m.HeaderLength, headerLengthValue)
	}
	m.PayloadLength = binary.BigEndian.Uint32(b[payloadLengthOffset:])
	if uint64(m.PayloadLength) != uint64(len(b)-int(m.HeaderLength)-hlLength) {
		return m, fmt.Errorf("payload length %d does not match message size %d", m.PayloadLength, len(b))
	}

	m.MessageType = strings.TrimSpace(strings.Trim(string(b[messageTypeOffset:messageTypeOffset+messageTypeLength]), "\x00"))
	m.SchemaVersion = binary.BigEndian.Uint32(b[schemaVersionOffset:])
	m.CreatedDate = binary.BigEndian.Uint64(b[createdDateOffset:])
	m.SequenceNumber = int64(binary.BigEndian.Uint64(b[sequenceNumberOffset:]))
	m.Flags = binary.BigEndian.Uint64(b[flagsOffset:])
	copy(m.MessageID[8:16], b[messageIDOffset:messageIDOffset+8])
	copy(m.MessageID[0:8], b[messageIDOffset+8:messageIDOffset+16])
	m.PayloadDigest = append([]byte(nil), b[payloadDigestOffset:payloadDigestOffset+payloadDigestLength]...)
	m.PayloadType = binary.BigEndian.Uint32(b[payloadTypeOffset:])
	m.Payload = append([]byte(nil), b[payloadOffset:]...)
	return m, nil
}

// Validate checks the header fields and, when there is a payload, its digest.
func (m *ClientMessage) Validate() error {
	if m.MessageType == StartPublicationMessage || m.MessageType == PausePublicationMessage {
		return nil
	}
	if m.HeaderLength == 0 {
		return errors.New("HeaderLength cannot be zero")
	}
	if m.MessageType == "" {
		return errors.New("MessageType is missing")
	}
	if m.CreatedDate == 0 {
		return errors.New("CreatedDate is missing")
	}
	if m.PayloadLength != 0 {
		digest := sha256.Sum256(m.Payload)
		if !bytes.Equal(digest[:], m.PayloadDigest) {
			return errors.New("payload Hash is not valid")
		}
	}
	return nil
}

// AckContent decodes the payload of an acknowledge message.
func (m *ClientMessage) AckContent() (AcknowledgeContent, error) {
	var v AcknowledgeContent
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		return v, fmt.Errorf("decode acknowledge content: %w", err)
	}
	return v, nil
}

// ChannelClosed decodes the payload of a channel_closed message.
func (m *ClientMessage) ChannelClosed() (ChannelClosed, error) {
	var v ChannelClosed
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		return v, fmt.Errorf("decode channel closed: %w", err)
	}
	return v, nil
}

// HandshakeRequest decodes the payload of a handshake request.
func (m *ClientMessage) HandshakeRequest() (HandshakeRequestPayload, error) {
	var v HandshakeRequestPayload
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		return v, fmt.Errorf("decode handshake request: %w", err)
	}
	return v, nil
}

// HandshakeComplete decodes the payload of a handshake complete message.
func (m *ClientMessage) HandshakeComplete() (HandshakeCompletePayload, error) {
	var v HandshakeCompletePayload
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		return v, fmt.Errorf("decode handshake complete: %w", err)
	}
	return v, nil
}

// NewAck builds the acknowledge message for a received message.
func NewAck(of ClientMessage, now time.Time) ClientMessage {
	// Marshalling a struct of strings, an int and a bool cannot fail.
	payload, _ := json.Marshal(AcknowledgeContent{
		MessageType:         of.MessageType,
		MessageID:           of.MessageID.String(),
		SequenceNumber:      of.SequenceNumber,
		IsSequentialMessage: true,
	})
	return ClientMessage{
		MessageType:    AcknowledgeMessage,
		SchemaVersion:  schemaVersion,
		CreatedDate:    uint64(now.UnixMilli()),
		SequenceNumber: 0,
		Flags:          FlagSYN | FlagFIN,
		MessageID:      uuid.New(),
		Payload:        payload,
	}
}

// NewInput builds an input_stream_data message.
func NewInput(seq int64, pt PayloadType, payload []byte, now time.Time) ClientMessage {
	return ClientMessage{
		MessageType:    InputStreamMessage,
		SchemaVersion:  schemaVersion,
		CreatedDate:    uint64(now.UnixMilli()),
		SequenceNumber: seq,
		Flags:          0,
		MessageID:      uuid.New(),
		PayloadType:    uint32(pt),
		Payload:        payload,
	}
}
