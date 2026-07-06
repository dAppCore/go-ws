// SPDX-Licence-Identifier: EUPL-1.2

package ws

import "testing"

func TestRedis_decodeRedisEnvelope_Good(t *testing.T) {
	payload := `{"sourceId":"src-1","message":{"type":"event"}}`
	env, ok := decodeRedisEnvelope(payload)
	if !ok {
		t.Fatalf("expected decode to succeed")
	}
	if env.SourceID != "src-1" {
		t.Errorf("expected src-1, got %s", env.SourceID)
	}
	if env.Message.Type != TypeEvent {
		t.Errorf("expected event, got %s", env.Message.Type)
	}
}

func TestRedis_decodeRedisEnvelope_Bad(t *testing.T) {
	// Empty payload and malformed JSON both decode to (zero, false).
	if _, ok := decodeRedisEnvelope(""); ok {
		t.Errorf("expected empty payload rejected")
	}
	if _, ok := decodeRedisEnvelope("{not json"); ok {
		t.Errorf("expected malformed JSON rejected")
	}
}

func TestRedis_decodeRedisEnvelope_Ugly(t *testing.T) {
	// A payload one byte over the cap is rejected before parsing.
	oversized := testRepeat("A", maxRedisEnvelopeBytes+1)
	if _, ok := decodeRedisEnvelope(oversized); ok {
		t.Errorf("expected oversized payload rejected")
	}
}

func TestRedis_validRedisForwardedMessage_Good(t *testing.T) {
	if !validRedisForwardedMessage(Message{Type: TypeEvent}) {
		t.Errorf("expected message without process ID accepted")
	}
	if !validRedisForwardedMessage(Message{Type: TypeProcessOutput, ProcessID: "proc-1"}) {
		t.Errorf("expected valid process ID accepted")
	}
}

func TestRedis_validRedisForwardedMessage_Bad(t *testing.T) {
	if validRedisForwardedMessage(Message{Type: TypeProcessOutput, ProcessID: "../escape"}) {
		t.Errorf("expected invalid process ID rejected")
	}
}

func TestRedis_validRedisForwardedMessage_Ugly(t *testing.T) {
	// An empty process ID is treated as "no process" and accepted.
	if !validRedisForwardedMessage(Message{Type: TypeEvent, ProcessID: ""}) {
		t.Errorf("expected empty process ID accepted")
	}
}
