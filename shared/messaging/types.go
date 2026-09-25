package messaging

import (
	pbd "github.com/cprakhar/uber-clone/shared/proto/driver"
	pb "github.com/cprakhar/uber-clone/shared/proto/trip"
)

type TripEventData struct {
	Trip *pb.Trip `json:"trip"`
}

type DriverTripResponseData struct {
	Driver  *pbd.Driver `json:"driver"`
	RiderID string      `json:"riderID"`
	TripID  string      `json:"tripID"`
}

type DriverLocationData struct {
	Location struct {
		Latitude  float64  `json:"latitude"`
		Longitude float64  `json:"longitude"`
		Heading   *float64 `json:"heading,omitempty"`
		Speed     *float64 `json:"speed,omitempty"`
	} `json:"location"`
}

type TripLifecycleCommand struct {
	TripID       string   `json:"tripID"`
	ActorID      string   `json:"actorID"`
	Reason       string   `json:"reason,omitempty"`
	Rating       int      `json:"rating,omitempty"`
	FeedbackTags []string `json:"feedbackTags,omitempty"`
	Comment      string   `json:"comment,omitempty"`
}

type PaymentEventSessionCreatedData struct {
	TripID               string  `json:"tripID"`
	SessionID            string  `json:"sessionID"`
	PaymentReference     string  `json:"paymentReference"`
	TransactionReference string  `json:"transactionReference"`
	CheckoutURL          string  `json:"checkoutURL"`
	Amount               float64 `json:"amount"`
	Currency             string  `json:"currency"`
}

type PaymentTripResponseData struct {
	TripID   string  `json:"tripID"`
	RiderID  string  `json:"riderID"`
	DriverID string  `json:"driverID"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type PaymentStatusUpdateData struct {
	EventID              string `json:"eventID"`
	TripID               string `json:"tripID"`
	RiderID              string `json:"riderID"`
	DriverID             string `json:"driverID"`
	PaymentReference     string `json:"paymentReference"`
	TransactionReference string `json:"transactionReference"`
	Status               string `json:"status"`
}
