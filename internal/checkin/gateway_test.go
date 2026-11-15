package checkin

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVerifyGatewaySignatureRejectsForgedAndReplayedRequests(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	secret := []byte("staging-secret")
	body := []byte(`{"event_id":"e1"}`)
	timestamp := "1700000000"
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifyGatewaySignature(timestamp, signature, body, secret, now, time.Minute) {
		t.Fatal("valid signature rejected")
	}
	if verifyGatewaySignature(timestamp, signature, body, []byte("wrong"), now, time.Minute) {
		t.Fatal("forged signature accepted")
	}
	if verifyGatewaySignature("1699990000", signature, body, secret, now, time.Minute) {
		t.Fatal("replayed timestamp accepted")
	}
}

func TestGatewayEventValidationRejectsNonDMAndClaims(t *testing.T) {
	base := GatewayEvent{EventID: "e1", Channel: "whatsapp", AccountID: "acct", SenderJID: "123@s.whatsapp.net", MessageID: "m1", Text: "skip"}
	if err := validateGatewayEvent(base); err != nil {
		t.Fatal(err)
	}
	for _, jid := range []string{"123@g.us", "status@broadcast", "x@newsletter"} {
		e := base
		e.SenderJID = jid
		if validateGatewayEvent(e) == nil {
			t.Fatalf("accepted non-DM sender %q", jid)
		}
	}
	// Unknown owner/workspace fields are rejected by the decoder, so claims
	// cannot be smuggled into the trusted transport envelope.
	body := []byte(`{"event_id":"e1","owner_id":"victim"}`)
	ts := "1700000000"
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.Header.Set("X-Mimo-Timestamp", ts)
	req.Header.Set("X-Mimo-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	GatewayHandler{Enabled: true, Secret: []byte("secret"), Clock: FixedClock{T: time.Unix(1700000000, 0)}}.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("unknown claims status=%d body=%s", rec.Code, rec.Body.String())
	}
}
