package ota

// CheckRequest is what a client tells the server when asking for an offer.
type CheckRequest struct {
	ClientID          string `json:"client_id"`
	Program           string `json:"program"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	Channel           string `json:"channel,omitempty"`
	RunningVersion    string `json:"running_version"`
	LastOfferIssuedAt int64  `json:"last_offer_issued_at"`
}

// SignedOffer is an offer's exact bytes plus the publisher's signature over
// them. The server passes both through untouched; it cannot mint either.
type SignedOffer struct {
	Offer     []byte `json:"offer"`
	Signature []byte `json:"signature"`
	Kind      string `json:"signature_kind"`
}
