package checkin

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultGatewayMaxBody = 64 << 10
	defaultGatewaySkew    = 5 * time.Minute
)

// GatewayEvent contains only identity and content supplied by the trusted
// gateway transport. Ownership is resolved from the server-side binding.
type GatewayEvent struct {
	EventID                  string `json:"event_id"`
	Channel                  string `json:"channel"`
	AccountID                string `json:"account_id"`
	SenderJID                string `json:"sender_jid"`
	MessageID                string `json:"message_id"`
	ReplyToProviderMessageID string `json:"reply_to_provider_message_id"`
	Text                     string `json:"text"`
	ReceivedAt               string `json:"received_at"`
}

type GatewayHandler struct {
	DB           *pgxpool.Pool
	Secret       []byte
	Enabled      bool
	Clock        Clock
	MaxBodyBytes int64
	MaxSkew      time.Duration
}

func (h GatewayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled || len(h.Secret) == 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		jsonGatewayError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	clock := h.Clock
	if clock == nil {
		clock = RealClock{}
	}
	maxBody := h.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultGatewayMaxBody
	}
	maxSkew := h.MaxSkew
	if maxSkew <= 0 {
		maxSkew = defaultGatewaySkew
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("checkin inbound rejected reason=body_read_failed")
		jsonGatewayError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	if !verifyGatewaySignature(r.Header.Get("X-Mimo-Timestamp"), r.Header.Get("X-Mimo-Signature"), body, h.Secret, clock.Now(), maxSkew) {
		log.Printf("checkin inbound rejected reason=invalid_signature")
		jsonGatewayError(w, http.StatusUnauthorized, "invalid gateway signature")
		return
	}
	var event GatewayEvent
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&event); err != nil {
		log.Printf("checkin inbound rejected reason=invalid_json")
		jsonGatewayError(w, http.StatusBadRequest, "invalid gateway event")
		return
	}
	if err := validateGatewayEvent(event); err != nil {
		log.Printf("checkin inbound rejected reason=invalid_event")
		jsonGatewayError(w, http.StatusBadRequest, "invalid gateway event")
		return
	}
	receivedAt := clock.Now()
	if event.ReceivedAt != "" {
		receivedAt, err = time.Parse(time.RFC3339Nano, event.ReceivedAt)
		if err != nil {
			jsonGatewayError(w, http.StatusBadRequest, "invalid received_at")
			return
		}
	}
	if h.DB == nil {
		jsonGatewayError(w, http.StatusServiceUnavailable, "inbound gateway unavailable")
		return
	}
	result, err := (&ResponseProcessor{DB: h.DB, Clock: clock}).Process(r.Context(), IncomingMessage{
		AccountID: event.AccountID, SenderJID: event.SenderJID, MessageID: event.MessageID,
		ReplyToProviderMessageID: event.ReplyToProviderMessageID, Text: event.Text, ReceivedAt: receivedAt,
	})
	if err != nil {
		// Do not disclose whether a sender, account, or check-in exists.
		log.Printf("checkin inbound rejected event_id=%s reason=processing_failed", event.EventID)
		jsonGatewayError(w, http.StatusForbidden, "inbound event rejected")
		return
	}
	log.Printf("checkin inbound accepted event_id=%s outcome=%s duplicate=%t", event.EventID, result.Outcome, result.Duplicate)
	jsonOKGateway(w, map[string]any{"accepted": true, "event_id": event.EventID, "outcome": result.Outcome, "duplicate": result.Duplicate})
}

func validateGatewayEvent(e GatewayEvent) error {
	if strings.TrimSpace(e.EventID) == "" || len(e.EventID) > 300 || e.Channel != "whatsapp" ||
		strings.TrimSpace(e.AccountID) == "" || len(e.AccountID) > 200 ||
		strings.TrimSpace(e.MessageID) == "" || len(e.MessageID) > 300 ||
		!directWhatsAppJID.MatchString(e.SenderJID) || strings.Contains(e.SenderJID, "@g.us") ||
		strings.Contains(e.SenderJID, "@broadcast") || strings.Contains(e.SenderJID, "@newsletter") ||
		strings.TrimSpace(e.Text) == "" || len(e.Text) > 16<<10 {
		return errors.New("invalid gateway event")
	}
	return nil
}

func verifyGatewaySignature(timestamp, signature string, body, secret []byte, now time.Time, maxSkew time.Duration) bool {
	if len(secret) == 0 || strings.TrimSpace(timestamp) == "" || strings.TrimSpace(signature) == "" {
		return false
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	stamp := time.Unix(seconds, 0)
	if absDuration(now.Sub(stamp)) > maxSkew {
		return false
	}
	provided := strings.TrimPrefix(strings.TrimSpace(signature), "sha256=")
	providedBytes, err := hex.DecodeString(provided)
	if err != nil || len(providedBytes) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return subtle.ConstantTimeCompare(providedBytes, mac.Sum(nil)) == 1
}

func absDuration(v time.Duration) time.Duration {
	if v < 0 {
		return -v
	}
	return v
}

func jsonGatewayError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func jsonOKGateway(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
